package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/probe"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
	"github.com/KuroKodaNinja/iceclimber/internal/webfetch"
)

// concAsker is a concurrency-aware fake asker: it records the peak number of prompts in flight
// at once (which must stay 1 — the console has a single modal) and blocks each prompt until
// released, so a serialization bug would show as overlap.
type concAsker struct {
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	asks        int
	release     chan struct{}
	reply       choice
}

func (a *concAsker) ask(prompt) choice {
	a.mu.Lock()
	a.inFlight++
	a.asks++
	if a.inFlight > a.maxInFlight {
		a.maxInFlight = a.inFlight
	}
	a.mu.Unlock()
	<-a.release
	a.mu.Lock()
	a.inFlight--
	a.mu.Unlock()
	return a.reply
}

func (a *concAsker) snapshot() (inFlight, maxInFlight, asks int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inFlight, a.maxInFlight, a.asks
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// C1 — concurrent gate() calls present ONE modal at a time (no clobber), and a first "approve
// all" collapses the queued duplicates to a single prompt (re-check under the prompt lock).
func TestApprover_ConcurrentGateSerializesAndCollapses(t *testing.T) {
	ca := &concAsker{release: make(chan struct{}), reply: choiceApproveRemember}
	ap := newApprover(ca, "sbx", activity.New(filepath.Join(t.TempDir(), "a.jsonl")))

	const m = 5
	errs := make(chan error, m)
	for i := 0; i < m; i++ {
		go func() { errs <- ap.gate(context.Background(), areq("pip.install", `{}`)) }()
	}
	// The first prompt is outstanding; no second may overlap it.
	waitFor(t, "the first prompt", func() bool { _, _, asks := ca.snapshot(); return asks >= 1 })
	if inFlight, _, _ := ca.snapshot(); inFlight > 1 {
		t.Fatalf("%d prompts in flight at once — the single modal would be clobbered", inFlight)
	}
	close(ca.release) // let the first ("approve all") complete → siblings should not prompt again
	for i := 0; i < m; i++ {
		if err := <-errs; err != nil {
			t.Errorf("remembered-approve gate returned %v, want nil", err)
		}
	}
	_, maxInFlight, asks := ca.snapshot()
	if maxInFlight != 1 {
		t.Errorf("max concurrent prompts = %d, want 1 (serialized)", maxInFlight)
	}
	if asks != 1 {
		t.Errorf("prompts shown = %d, want 1 ('approve all' must collapse the queued duplicates)", asks)
	}
}

// C2 — a gate() and an ApproveFetch() needing a modal at the same time serialize on the shared
// prompt lock (both funnel through present()), so a proxy egress approval and an install
// approval can't clobber each other's modal.
func TestApprover_GateAndFetchSerialize(t *testing.T) {
	ca := &concAsker{release: make(chan struct{}), reply: choiceApproveOnce}
	ap := newApprover(ca, "sbx", activity.New(filepath.Join(t.TempDir(), "a.jsonl")))

	done := make(chan struct{}, 2)
	go func() { _ = ap.gate(context.Background(), areq("pip.install", `{}`)); done <- struct{}{} }()
	go func() {
		ap.ApproveFetch(context.Background(), webfetch.ApprovalPrompt{Host: "x.com", URL: "https://x.com/y", Method: "GET"})
		done <- struct{}{}
	}()
	waitFor(t, "the first prompt", func() bool { _, _, asks := ca.snapshot(); return asks >= 1 })
	if inFlight, _, _ := ca.snapshot(); inFlight > 1 {
		t.Fatalf("gate and ApproveFetch prompted concurrently (inFlight=%d) — modal clobber", inFlight)
	}
	close(ca.release)
	<-done
	<-done
	if _, maxInFlight, asks := ca.snapshot(); maxInFlight != 1 || asks != 2 {
		t.Errorf("maxInFlight=%d asks=%d, want 1/2 (both prompted, serialized)", maxInFlight, asks)
	}
}

// C3 — the headless serve observers (stdout receipt + serviced line) fire from N worker
// goroutines at once; run under `go test -race`, this catches an unguarded write to the shared
// io.Writer (the obsMu regression guard). Uses a real buildServeDispatcher over a local ExecFS.
func TestServeDispatcher_ConcurrentObserversNoRace(t *testing.T) {
	ctx := context.Background()
	fs := remotefs.NewExecFS(remotefstest.LocalRunner{})
	tree := protocol.Tree{Root: t.TempDir()}
	if err := protocol.EnsureTree(ctx, fs, tree); err != nil {
		t.Fatal(err)
	}
	sess := &session{fs: fs, tree: tree, fp: &probe.Fingerprint{OS: "linux", Arch: "arm64"},
		cacheDir: t.TempDir(), sandboxID: "sbx", dispatchConcur: 8}
	cfg := &config.Config{SandboxID: "sbx", DispatchConcurrency: 8}

	var out bytes.Buffer // the shared writer both observers race on if unguarded
	act := activity.New(filepath.Join(t.TempDir(), "activity.jsonl"))
	defer act.Close()
	disp := buildServeDispatcher(ctx, sess, cfg, nil, &out, false, act)

	const nreq = 24
	for i := 0; i < nreq; i++ {
		id := fmt.Sprintf("png%02d", i)
		req := protocol.Request{SchemaVersion: protocol.SchemaVersion, ID: id, Type: "ping", CreatedAt: time.Now().UTC(), Params: []byte("{}")}
		b, _ := json.Marshal(req)
		if err := protocol.Deliver(ctx, fs, tree.Outbox(), protocol.RequestName(id), b); err != nil {
			t.Fatal(err)
		}
	}
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- disp.Serve(sctx, 10*time.Millisecond) }()

	// Wait until all pings are answered (their serviced lines all written concurrently).
	waitFor(t, "all pings serviced", func() bool {
		names, _ := fs.List(ctx, tree.Inbox().New())
		return len(names) >= nreq
	})
	cancel()
	<-done
}
