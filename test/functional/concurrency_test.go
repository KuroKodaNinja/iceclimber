//go:build functional

package functional

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Concurrency is proven end-to-end by reconstructing each handler's execution INTERVAL from
// recorded data (no fragile wall-clock timing): the response's CompletedAt (ns) is the end, the
// activity-log serviced entry's DurMS is the duration, so start = end − DurMS. Overlap and
// max-in-flight then follow from interval math. A background `serve` runs under a private HOME so
// its activity.jsonl is isolated (the reconnect_test pattern; safeBuffer is shared).

type ivl struct {
	id, typ    string
	start, end time.Time
}

// concConfig writes a sandbox config pinned to a root for the concurrency tests: an optional
// dispatch_concurrency (n>0), and egress_mode relay unless the test needs the proxy (only the
// proxy-egress test does — relay avoids needless reverse-tunnel churn across the many serves).
func concConfig(t *testing.T, sb sandboxConn, root string, n int, relay bool) string {
	t.Helper()
	c := sshConfigYAML(sb) + fmt.Sprintf("remote_root: %s\n", root)
	if n > 0 {
		c += fmt.Sprintf("dispatch_concurrency: %d\n", n)
	}
	if relay {
		c += "egress_mode: relay\n"
	}
	scheduleRootCleanupOn(t, sb.Name, root)
	return writeYAML(t, c)
}

// startConcServe launches a long-lived `serve --yes` under a private HOME and returns that HOME
// (for its activity.jsonl) plus a stop func.
func startConcServe(t *testing.T, cfg, transport string) (home string, stop func()) {
	t.Helper()
	home = t.TempDir()
	var out safeBuffer
	cmd := exec.Command(iceclimberBin, "serve", "--yes", "--config", cfg, "--transport", transport)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	return home, func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }
}

// deliverReq writes one request into outbox/new and returns its id.
func deliverReq(t *testing.T, ctx context.Context, fs remotefs.FS, tree protocol.Tree, typ, params string) string {
	t.Helper()
	id := protocol.NewID()
	data, _ := json.Marshal(protocol.Request{
		SchemaVersion: protocol.SchemaVersion, ID: id, Type: typ,
		CreatedAt: time.Now().UTC(), Params: json.RawMessage(params),
	})
	if err := protocol.Deliver(ctx, fs, tree.Outbox(), protocol.RequestName(id), data); err != nil {
		t.Fatalf("deliver %s: %v", typ, err)
	}
	return id
}

// waitResponses polls until every id has a response in the inbox (or times out).
func waitResponses(t *testing.T, ctx context.Context, fs remotefs.FS, tree protocol.Tree, ids []string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		done := 0
		for _, id := range ids {
			if r, err := protocol.ReadResponse(ctx, fs, tree, protocol.RequestName(id)); err == nil && r != nil {
				done++
			}
		}
		if done == len(ids) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("not all %d responses arrived within %v", len(ids), within)
}

// intervals reconstructs each request's execution window from the response CompletedAt (end) and
// the activity-log DurMS. It polls the activity log briefly since the serviced line is appended
// just after the response is delivered.
func intervals(t *testing.T, ctx context.Context, fs remotefs.FS, tree protocol.Tree, home, sandboxID string, ids []string) map[string]ivl {
	t.Helper()
	actPath := filepath.Join(home, ".iceclimber", sandboxID, "activity.jsonl")
	dur := map[string]int64{}
	typ := map[string]string{}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		evs, _ := activity.Read(actPath)
		for _, e := range evs {
			if e.Kind == activity.KindServiced {
				dur[e.ID] = e.DurMS
				typ[e.ID] = e.Type
			}
		}
		all := true
		for _, id := range ids {
			if _, ok := dur[id]; !ok {
				all = false
			}
		}
		if all {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	out := map[string]ivl{}
	for _, id := range ids {
		r, err := protocol.ReadResponse(ctx, fs, tree, protocol.RequestName(id))
		if err != nil || r == nil {
			t.Fatalf("no response for %s", id)
		}
		out[id] = ivl{id: id, typ: typ[id], end: r.CompletedAt, start: r.CompletedAt.Add(-time.Duration(dur[id]) * time.Millisecond)}
	}
	return out
}

func overlap(a, b ivl) bool { return a.start.Before(b.end) && b.start.Before(a.end) }

// maxConcurrent is the peak number of intervals overlapping at any instant (sweep line).
func maxConcurrent(ivls []ivl) int {
	type ev struct {
		at    time.Time
		delta int
	}
	var evs []ev
	for _, v := range ivls {
		evs = append(evs, ev{v.start, 1}, ev{v.end, -1})
	}
	sort.Slice(evs, func(i, j int) bool {
		if evs[i].at.Equal(evs[j].at) {
			return evs[i].delta < evs[j].delta // process ends before starts at the same instant
		}
		return evs[i].at.Before(evs[j].at)
	})
	cur, max := 0, 0
	for _, e := range evs {
		cur += e.delta
		if cur > max {
			max = cur
		}
	}
	return max
}

// F1 — quick ops never queue behind a slow install. A python.install (delivered first) plus five
// pings: every ping completes BEFORE the install (impossible under the old oldest-first
// sequential dispatch, where the install would finish first). Run over both transports.
func TestConcurrent_QuickOpsNotBlocked(t *testing.T) {
	sb := requireSandbox(t)
	for _, transport := range []string{"sftp", "exec"} {
		t.Run(transport, func(t *testing.T) {
			root := "/tmp/iceclimber-conc1-" + protocol.NewID()
			cfg := concConfig(t, sb, root, 0, true)
			runIceclimber(t, "bootstrap", "--config", cfg, "--transport", transport)
			home, stop := startConcServe(t, cfg, transport)
			defer stop()
			_ = home

			fs, cleanup := dialFS(t, sb, transport)
			defer cleanup()
			ctx := context.Background()
			tree := protocol.Tree{Root: root}

			pyID := deliverReq(t, ctx, fs, tree, "python.install", `{"version":"3.12"}`)
			var pings []string
			for i := 0; i < 5; i++ {
				pings = append(pings, deliverReq(t, ctx, fs, tree, "ping", "{}"))
			}
			all := append([]string{pyID}, pings...)
			waitResponses(t, ctx, fs, tree, all, 3*time.Minute)

			ends := map[string]time.Time{}
			for _, id := range all {
				r, _ := protocol.ReadResponse(ctx, fs, tree, protocol.RequestName(id))
				ends[id] = r.CompletedAt
			}
			for _, p := range pings {
				if !ends[p].Before(ends[pyID]) {
					t.Errorf("ping %s completed at %v, not before the install at %v — quick ops blocked behind the install",
						p, ends[p].Format(time.RFC3339Nano), ends[pyID].Format(time.RFC3339Nano))
				}
			}
		})
	}
}

// F2 — different-runtime installs run in parallel: python.install and node.install overlap in
// time (proving real concurrent handler execution, not just interleaved pickup).
func TestConcurrent_HeavyParallelism(t *testing.T) {
	sb := requireGlibcSandbox(t) // has node
	root := "/tmp/iceclimber-conc2-" + protocol.NewID()
	cfg := concConfig(t, sb, root, 0, true)
	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")
	home, stop := startConcServe(t, cfg, "sftp")
	defer stop()

	fs, cleanup := dialFS(t, sb, "sftp")
	defer cleanup()
	ctx := context.Background()
	tree := protocol.Tree{Root: root}

	py := deliverReq(t, ctx, fs, tree, "python.install", `{"version":"3.12"}`)
	nd := deliverReq(t, ctx, fs, tree, "node.install", `{"version":"24"}`)
	waitResponses(t, ctx, fs, tree, []string{py, nd}, 5*time.Minute)
	iv := intervals(t, ctx, fs, tree, home, sb.Name, []string{py, nd})
	if !overlap(iv[py], iv[nd]) {
		t.Errorf("python and node installs did not overlap — not running in parallel:\n  py=%v..%v\n  nd=%v..%v",
			iv[py].start, iv[py].end, iv[nd].start, iv[nd].end)
	}
}

// F3 — the cap binds: with dispatch_concurrency=2, three different-runtime installs never have
// more than two running at once (the third queues), yet all three complete.
func TestConcurrent_CapEnforced(t *testing.T) {
	sb := requireGlibcSandbox(t) // has node + java
	root := "/tmp/iceclimber-conc3-" + protocol.NewID()
	cfg := concConfig(t, sb, root, 2, true)
	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")
	home, stop := startConcServe(t, cfg, "sftp")
	defer stop()

	fs, cleanup := dialFS(t, sb, "sftp")
	defer cleanup()
	ctx := context.Background()
	tree := protocol.Tree{Root: root}

	ids := []string{
		deliverReq(t, ctx, fs, tree, "python.install", `{"version":"3.12"}`),
		deliverReq(t, ctx, fs, tree, "node.install", `{"version":"24"}`),
		deliverReq(t, ctx, fs, tree, "java.install", `{"version":"21"}`),
	}
	waitResponses(t, ctx, fs, tree, ids, 8*time.Minute)
	iv := intervals(t, ctx, fs, tree, home, sb.Name, ids)
	list := []ivl{iv[ids[0]], iv[ids[1]], iv[ids[2]]}
	if got := maxConcurrent(list); got != 2 {
		t.Errorf("max concurrent installs = %d, want 2 (the cap must bind, and all three must complete)", got)
	}
}

// F4 — same-runtime requests serialize while different runtimes parallelize. With the cap
// non-binding (4), install python, then two pip.installs into that same Python plus a node
// install: the two pip intervals must NOT overlap each other (Popo serializes the shared env),
// but each overlaps the node install (a different key).
func TestConcurrent_KeyedSerialization(t *testing.T) {
	sb := requireGlibcSandbox(t)
	root := "/tmp/iceclimber-conc4-" + protocol.NewID()
	cfg := concConfig(t, sb, root, 4, true)
	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")
	// Python must exist before pip installs into it.
	runIceclimber(t, "install", "python", "3.12", "--config", cfg, "--transport", "sftp")
	home, stop := startConcServe(t, cfg, "sftp")
	defer stop()

	fs, cleanup := dialFS(t, sb, "sftp")
	defer cleanup()
	ctx := context.Background()
	tree := protocol.Tree{Root: root}

	pip1 := deliverReq(t, ctx, fs, tree, "pip.install", `{"python_version":"3.12","packages":[{"name":"six"}]}`)
	pip2 := deliverReq(t, ctx, fs, tree, "pip.install", `{"python_version":"3.12","packages":[{"name":"idna"}]}`)
	nd := deliverReq(t, ctx, fs, tree, "node.install", `{"version":"24"}`)
	ids := []string{pip1, pip2, nd}
	waitResponses(t, ctx, fs, tree, ids, 5*time.Minute)
	iv := intervals(t, ctx, fs, tree, home, sb.Name, ids)

	if overlap(iv[pip1], iv[pip2]) {
		t.Errorf("two pip installs into the same Python overlapped — same-runtime serialization broken:\n  pip1=%v..%v\n  pip2=%v..%v",
			iv[pip1].start, iv[pip1].end, iv[pip2].start, iv[pip2].end)
	}
	if !overlap(iv[pip1], iv[nd]) && !overlap(iv[pip2], iv[nd]) {
		t.Errorf("neither pip overlapped the node install — different-key parallelism lost")
	}
}

// F5 — dedup under concurrency: the same request id delivered twice yields exactly one response.
func TestConcurrent_DedupUnderLoad(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-conc5-" + protocol.NewID()
	cfg := concConfig(t, sb, root, 0, true)
	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")
	home, stop := startConcServe(t, cfg, "sftp")
	defer stop()
	_ = home

	fs, cleanup := dialFS(t, sb, "sftp")
	defer cleanup()
	ctx := context.Background()
	tree := protocol.Tree{Root: root}

	// Deliver the same id twice (a retry), plus a couple of distinct pings for load.
	id := protocol.NewID()
	name := protocol.RequestName(id)
	data, _ := json.Marshal(protocol.Request{SchemaVersion: protocol.SchemaVersion, ID: id, Type: "ping", CreatedAt: time.Now().UTC(), Params: json.RawMessage("{}")})
	if err := protocol.Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
		t.Fatal(err)
	}
	if err := protocol.Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
		t.Fatal(err)
	}
	waitResponses(t, ctx, fs, tree, []string{id}, time.Minute)
	// After servicing settles, exactly one serviced event for this id in the activity log.
	time.Sleep(1 * time.Second)
	evs, _ := activity.Read(filepath.Join(home, ".iceclimber", sb.Name, "activity.jsonl"))
	n := 0
	for _, e := range evs {
		if e.Kind == activity.KindServiced && e.ID == id {
			n++
		}
	}
	if n != 1 {
		t.Errorf("duplicate-id delivery serviced %d times, want 1", n)
	}
}

// F7 — concurrent egress through the MITM proxy (the default egress mode). With python + node
// installed, a pip.install and an npm.install run their native tools through the proxy at the
// same time: both succeed and their intervals overlap (concurrent proxy egress, no deadlock).
func TestConcurrent_ProxyEgress(t *testing.T) {
	sb := requireGlibcSandbox(t) // has node; proxy is the default egress mode
	root := "/tmp/iceclimber-conc7-" + protocol.NewID()
	cfg := concConfig(t, sb, root, 0, false)
	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")
	runIceclimber(t, "install", "python", "3.12", "--config", cfg, "--transport", "sftp")
	runIceclimber(t, "install", "node", "24", "--config", cfg, "--transport", "sftp")
	home, stop := startConcServe(t, cfg, "sftp") // brings up the egress proxy tunnel
	defer stop()

	fs, cleanup := dialFS(t, sb, "sftp")
	defer cleanup()
	ctx := context.Background()
	tree := protocol.Tree{Root: root}

	pip := deliverReq(t, ctx, fs, tree, "pip.install", `{"python_version":"3.12","packages":[{"name":"six"}]}`)
	npm := deliverReq(t, ctx, fs, tree, "npm.install", `{"node_version":"24","packages":[{"name":"left-pad"}]}`)
	ids := []string{pip, npm}
	waitResponses(t, ctx, fs, tree, ids, 5*time.Minute)

	for _, id := range ids {
		r, _ := protocol.ReadResponse(ctx, fs, tree, protocol.RequestName(id))
		if r.Status != protocol.StatusOK {
			t.Fatalf("proxy egress %s failed: status=%q err=%+v", r.ID, r.Status, r.Error)
		}
	}
	iv := intervals(t, ctx, fs, tree, home, sb.Name, ids)
	if !overlap(iv[pip], iv[npm]) {
		t.Errorf("pip and npm proxy egress did not overlap — concurrent proxy egress not working:\n  pip=%v..%v\n  npm=%v..%v",
			iv[pip].start, iv[pip].end, iv[npm].start, iv[npm].end)
	}
}
