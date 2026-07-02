package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

// These tests exercise the bounded worker pool (SetConcurrency) with controllable fake handlers
// under `go test -race`, proving the invariants the live functional tests can only observe
// approximately: exact parallelism, the cap, in-flight dedup, keyed serialization, and the
// error/panic/shutdown plumbing.

// peak tracks the max simultaneous handlers (concurrency observed).
type peak struct{ cur, max int32 }

func (p *peak) enter() {
	c := atomic.AddInt32(&p.cur, 1)
	for {
		m := atomic.LoadInt32(&p.max)
		if c <= m || atomic.CompareAndSwapInt32(&p.max, m, c) {
			break
		}
	}
}
func (p *peak) leave() { atomic.AddInt32(&p.cur, -1) }

func concSetup(t *testing.T, reg Registry, n int) (context.Context, remotefs.FS, Tree, *Dispatcher) {
	t.Helper()
	ctx := context.Background()
	fs := remotefs.NewExecFS(remotefstest.LocalRunner{})
	tree := Tree{Root: t.TempDir()}
	if err := EnsureTree(ctx, fs, tree); err != nil {
		t.Fatalf("EnsureTree: %v", err)
	}
	d := NewDispatcher(fs, tree, reg)
	d.SetConcurrency(n)
	return ctx, fs, tree, d
}

func deliverReq(t *testing.T, ctx context.Context, fs remotefs.FS, tree Tree, id, typ, params string) {
	t.Helper()
	req := Request{SchemaVersion: SchemaVersion, ID: id, Type: typ, CreatedAt: time.Now().UTC(), Params: json.RawMessage(params)}
	b, _ := json.Marshal(req)
	if err := Deliver(ctx, fs, tree.Outbox(), RequestName(id), b); err != nil {
		t.Fatalf("deliver %s: %v", id, err)
	}
}

// serveBG runs Serve on a goroutine at a tight poll interval and returns a stop() that cancels
// and waits for a clean return.
func serveBG(t *testing.T, ctx context.Context, d *Dispatcher) (done chan error, cancel context.CancelFunc) {
	sctx, cancel := context.WithCancel(ctx)
	done = make(chan error, 1)
	go func() { done <- d.Serve(sctx, 15*time.Millisecond) }()
	return done, cancel
}

// U1 — with N workers, N independent requests run at once (a barrier that only releases when all
// N have entered would deadlock under sequential dispatch).
func TestConcurrent_Parallel(t *testing.T) {
	const n = 4
	entered := make(chan struct{}, n)
	release := make(chan struct{})
	reg := Registry{"work": func(_ context.Context, req Request) Response {
		entered <- struct{}{}
		<-release
		return OK(req.ID, nil)
	}}
	ctx, fs, tree, d := concSetup(t, reg, n)
	for i := 0; i < n; i++ {
		deliverReq(t, ctx, fs, tree, fmt.Sprintf("w%d", i), "work", "{}")
	}
	done, cancel := serveBG(t, ctx, d)
	for i := 0; i < n; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d/%d handlers entered concurrently — parallelism broken", i, n)
		}
	}
	close(release)
	cancel()
	<-done
}

// U2 — the pool never runs more than N handlers at once.
func TestConcurrent_CapEnforced(t *testing.T) {
	const n = 2
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var p peak
	reg := Registry{"work": func(_ context.Context, req Request) Response {
		p.enter()
		entered <- struct{}{}
		<-release
		p.leave()
		return OK(req.ID, nil)
	}}
	ctx, fs, tree, d := concSetup(t, reg, n)
	for i := 0; i < 4; i++ {
		deliverReq(t, ctx, fs, tree, fmt.Sprintf("w%d", i), "work", "{}")
	}
	done, cancel := serveBG(t, ctx, d)
	// Two enter; a third must NOT within a grace window (the cap holds it back).
	<-entered
	<-entered
	select {
	case <-entered:
		t.Fatal("a third handler entered — cap of 2 not enforced")
	case <-time.After(250 * time.Millisecond):
	}
	close(release)
	cancel()
	<-done
	if got := atomic.LoadInt32(&p.max); got != n {
		t.Errorf("peak concurrency = %d, want %d", got, n)
	}
}

// U3 — a request re-delivered while its handler is still running is not serviced twice.
func TestConcurrent_InFlightDedup(t *testing.T) {
	var runs int32
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	reg := Registry{"work": func(_ context.Context, req Request) Response {
		atomic.AddInt32(&runs, 1)
		entered <- struct{}{}
		<-release
		return OK(req.ID, nil)
	}}
	ctx, fs, tree, d := concSetup(t, reg, 4)
	deliverReq(t, ctx, fs, tree, "x", "work", "{}")
	done, cancel := serveBG(t, ctx, d)
	<-entered // handler for x is now in flight (blocked)
	// Re-deliver the SAME id while its handler runs; give the drain a few cycles to see it.
	deliverReq(t, ctx, fs, tree, "x", "work", "{}")
	time.Sleep(150 * time.Millisecond)
	close(release)
	cancel()
	<-done
	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Errorf("handler ran %d times for a re-delivered in-flight request, want 1", got)
	}
}

// U4 — same-key requests serialize (never overlap); different-key requests run in parallel.
func TestConcurrent_KeyedSerialization(t *testing.T) {
	newReg := func(p *peak, entered chan struct{}, release chan struct{}) Registry {
		h := func(_ context.Context, req Request) Response {
			p.enter()
			entered <- struct{}{}
			<-release
			p.leave()
			return OK(req.ID, nil)
		}
		return Registry{"python.install": h, "node.install": h}
	}

	t.Run("same key serializes", func(t *testing.T) {
		var p peak
		entered := make(chan struct{}, 4)
		release := make(chan struct{})
		ctx, fs, tree, d := concSetup(t, newReg(&p, entered, release), 4) // cap non-binding
		deliverReq(t, ctx, fs, tree, "p1", "python.install", `{"version":"3.12"}`)
		deliverReq(t, ctx, fs, tree, "p2", "python.install", `{"version":"3.12"}`)
		done, cancel := serveBG(t, ctx, d)
		<-entered // one enters
		select {
		case <-entered:
			t.Fatal("both same-key installs ran at once — keyed serialization broken")
		case <-time.After(250 * time.Millisecond):
		}
		close(release) // first finishes → key frees → second runs
		<-entered
		cancel()
		<-done
		if got := atomic.LoadInt32(&p.max); got != 1 {
			t.Errorf("same-key peak concurrency = %d, want 1", got)
		}
	})

	t.Run("different key parallelizes", func(t *testing.T) {
		var p peak
		entered := make(chan struct{}, 4)
		release := make(chan struct{})
		ctx, fs, tree, d := concSetup(t, newReg(&p, entered, release), 4)
		deliverReq(t, ctx, fs, tree, "a", "python.install", `{"version":"3.12"}`)
		deliverReq(t, ctx, fs, tree, "b", "node.install", `{"version":"20"}`)
		done, cancel := serveBG(t, ctx, d)
		<-entered
		<-entered // both entered → they overlap
		close(release)
		cancel()
		<-done
		if got := atomic.LoadInt32(&p.max); got != 2 {
			t.Errorf("different-key peak concurrency = %d, want 2", got)
		}
	})
}

// faultFS injects a WriteFile failure for paths matching failOn — to simulate a dropped link
// during a worker's response Deliver.
type faultFS struct {
	remotefs.FS
	failOn func(path string) bool
}

func (f faultFS) WriteFile(ctx context.Context, p string, data []byte) error {
	if f.failOn(p) {
		return errors.New("simulated transport failure")
	}
	return f.FS.WriteFile(ctx, p, data)
}

// U5 — a worker's transport error (a failed response Deliver) makes Serve return it (→ reconnect),
// and teardown waits for the other in-flight workers.
func TestConcurrent_WorkerTransportError(t *testing.T) {
	release := make(chan struct{})
	var okDone int32
	reg := Registry{"work": func(ctx context.Context, req Request) Response {
		if req.ID == "bad" {
			return OK(req.ID, nil) // handler fine; the Deliver of its response will fail
		}
		// The good worker stays in flight until released OR teardown cancels workCtx (which the
		// bad worker's error triggers) — so Serve's wg.Wait can drain it.
		select {
		case <-release:
		case <-ctx.Done():
		}
		atomic.AddInt32(&okDone, 1)
		return OK(req.ID, nil)
	}}
	ctx := context.Background()
	base := remotefs.NewExecFS(remotefstest.LocalRunner{})
	tree := Tree{Root: t.TempDir()}
	if err := EnsureTree(ctx, base, tree); err != nil {
		t.Fatal(err)
	}
	// Fail only the INBOX response write for "bad" (not its outbox request delivery), simulating
	// a link drop during the worker's Deliver.
	fs := faultFS{FS: base, failOn: func(p string) bool { return strings.Contains(p, "inbox") && strings.Contains(p, "bad") }}
	d := NewDispatcher(fs, tree, reg)
	d.SetConcurrency(2)

	deliverReq(t, ctx, fs, tree, "bad", "work", "{}")
	deliverReq(t, ctx, fs, tree, "good", "work", "{}")
	done, cancel := serveBG(t, ctx, d)
	defer cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "deliver") {
			t.Fatalf("Serve err = %v, want a transport (deliver) error", err)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("a worker transport error should make Serve return promptly")
	}
	close(release) // let the good worker finish (teardown waits for it)
}

// U6 — a handler panic becomes an error Response, not a daemon crash.
func TestConcurrent_PanicGuard(t *testing.T) {
	reg := Registry{"work": func(_ context.Context, _ Request) Response { panic("boom") }}
	ctx, fs, tree, d := concSetup(t, reg, 2)
	deliverReq(t, ctx, fs, tree, "p", "work", "{}")
	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce should not surface a handler panic as an error: %v", err)
	}
	resp, err := ReadResponse(ctx, fs, tree, RequestName("p"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != StatusError || resp.Error == nil || resp.Error.Code != "handler_panic" {
		t.Errorf("panic should yield a handler_panic error response, got %+v", resp)
	}
}

// U7 — cancelling serve with a worker in flight returns promptly (handlers honor ctx).
func TestConcurrent_ShutdownDrains(t *testing.T) {
	entered := make(chan struct{}, 1)
	reg := Registry{"work": func(ctx context.Context, req Request) Response {
		entered <- struct{}{}
		<-ctx.Done() // block until teardown cancels workCtx
		return OK(req.ID, nil)
	}}
	ctx, fs, tree, d := concSetup(t, reg, 2)
	deliverReq(t, ctx, fs, tree, "w", "work", "{}")
	done, cancel := serveBG(t, ctx, d)
	<-entered
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return promptly after cancel (worker not drained)")
	}
}

// U8 — RunOnce is synchronous: the response exists the instant it returns (it waited for the
// worker), even for a slow handler.
func TestConcurrent_RunOnceWaits(t *testing.T) {
	reg := Registry{"work": func(_ context.Context, req Request) Response {
		time.Sleep(80 * time.Millisecond)
		return OK(req.ID, nil)
	}}
	ctx, fs, tree, d := concSetup(t, reg, 4)
	deliverReq(t, ctx, fs, tree, "s", "work", "{}")
	if err := d.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadResponse(ctx, fs, tree, RequestName("s")); err != nil {
		t.Errorf("RunOnce returned before the handler finished (not synchronous): %v", err)
	}
}
