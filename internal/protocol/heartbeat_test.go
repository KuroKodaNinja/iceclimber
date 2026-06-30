package protocol

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestWriteHeartbeat_AdvancesSeqAndFires: each write bumps seq, fires OnHeartbeat with
// the new seq, and publishes "<seq> <rfc3339>".
func TestWriteHeartbeat_AdvancesSeqAndFires(t *testing.T) {
	ctx, fs, tree, d := setup(t)
	var got int64
	d.OnHeartbeat(func(seq int64) { got = seq })

	for i := int64(1); i <= 3; i++ {
		if err := d.WriteHeartbeat(ctx); err != nil {
			t.Fatalf("WriteHeartbeat: %v", err)
		}
		if got != i {
			t.Errorf("OnHeartbeat seq = %d, want %d", got, i)
		}
	}
	data, err := fs.ReadFile(ctx, tree.Heartbeat())
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) < 2 || fields[0] != "3" {
		t.Fatalf("heartbeat content = %q, want \"3 <ts>\"", data)
	}
	if _, err := time.Parse(time.RFC3339, fields[1]); err != nil {
		t.Errorf("heartbeat timestamp not RFC3339: %q", fields[1])
	}
}

// TestServe_HeartbeatNotStarvedByLongHandler is the root-cause regression: a handler
// that blocks the single-threaded drain loop must NOT stall the heartbeat — it runs on
// its own goroutine now, so seq keeps advancing while the long handler is in flight.
func TestServe_HeartbeatNotStarvedByLongHandler(t *testing.T) {
	ctx, fs, tree, _ := setup(t)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	started := make(chan struct{})
	release := make(chan struct{})
	reg := Registry{
		"slow": func(_ context.Context, req Request) Response {
			close(started)
			<-release // hold the drain loop hostage
			return OK(req.ID, map[string]string{})
		},
	}
	d := NewDispatcher(fs, tree, reg)
	var beats int32
	d.OnHeartbeat(func(int64) { atomic.AddInt32(&beats, 1) })

	id := NewID()
	if err := Deliver(ctx, fs, tree.Outbox(), RequestName(id), request(id, "slow")); err != nil {
		t.Fatal(err)
	}
	go func() { _ = d.Serve(ctx, 20*time.Millisecond) }()

	<-started // the slow handler is now blocking drainNew
	ok := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if atomic.LoadInt32(&beats) >= 3 {
			ok = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release) // unblock the handler before asserting (so nothing leaks)
	cancel()
	if !ok {
		t.Fatalf("heartbeat starved by the blocked handler: only %d beats in 3s", atomic.LoadInt32(&beats))
	}
}
