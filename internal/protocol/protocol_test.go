package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

// setup builds a dispatcher backed by a real ExecFS over the host shell (same
// code path the VM exercises), rooted at a fresh temp dir.
func setup(t *testing.T) (context.Context, remotefs.FS, Tree, *Dispatcher) {
	t.Helper()
	ctx := context.Background()
	fs := remotefs.NewExecFS(remotefstest.LocalRunner{})
	tree := Tree{Root: t.TempDir()}
	if err := EnsureTree(ctx, fs, tree); err != nil {
		t.Fatalf("EnsureTree: %v", err)
	}
	return ctx, fs, tree, NewDispatcher(fs, tree, Registry{"ping": PingHandler("9.9.9-test")})
}

func request(id, typ string) []byte {
	req := Request{
		SchemaVersion: SchemaVersion,
		ID:            id,
		Type:          typ,
		CreatedAt:     time.Now().UTC(),
		Params:        json.RawMessage("{}"),
	}
	b, _ := json.Marshal(req)
	return b
}

func TestDispatch_PingRoundTrip(t *testing.T) {
	ctx, fs, tree, d := setup(t)
	id := NewID()
	name := RequestName(id)
	if err := Deliver(ctx, fs, tree.Outbox(), name, request(id, "ping")); err != nil {
		t.Fatal(err)
	}
	if err := d.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	resp, err := ReadResponse(ctx, fs, tree, name)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	if resp.Status != StatusOK || resp.ID != id {
		t.Errorf("resp = %+v, want ok with id %s", resp, id)
	}
	var pr pingResult
	if err := json.Unmarshal(resp.Result, &pr); err != nil {
		t.Fatalf("unmarshal pong: %v", err)
	}
	if pr.PopoVersion != "9.9.9-test" {
		t.Errorf("popo_version = %q, want 9.9.9-test", pr.PopoVersion)
	}
}

func TestDispatch_Dedup(t *testing.T) {
	ctx, fs, tree, d := setup(t)
	id := NewID()
	name := RequestName(id)
	mustDeliver := func() {
		if err := Deliver(ctx, fs, tree.Outbox(), name, request(id, "ping")); err != nil {
			t.Fatal(err)
		}
	}
	mustDeliver()
	if err := d.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := fs.ReadFile(ctx, path.Join(tree.Inbox().New(), name))
	if err != nil {
		t.Fatal(err)
	}

	// Re-deliver the same id; a correct dispatcher must NOT regenerate the response.
	mustDeliver()
	if err := d.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := fs.ReadFile(ctx, path.Join(tree.Inbox().New(), name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Error("response was regenerated on re-delivery; dedup failed")
	}
	if names, _ := fs.List(ctx, tree.Outbox().New()); len(names) != 0 {
		t.Errorf("outbox/new not drained: %v", names)
	}
}

func TestDispatch_ObserveStart_FiresAtPickup(t *testing.T) {
	// The start hook must fire once per request, BEFORE the terminal observer, for both
	// the drainNew path and the recover (stranded-in-cur) path — covering agent- and
	// operator-initiated requests alike (the dispatcher doesn't distinguish them).
	run := func(t *testing.T, seed func(ctx context.Context, fs remotefs.FS, tree Tree, name string, data []byte)) StartEvent {
		ctx, fs, tree, d := setup(t)
		id := NewID()
		name := RequestName(id)
		var order []string
		var got StartEvent
		var startCount int
		d.ObserveStart(func(ev StartEvent) {
			order = append(order, "start")
			got = ev
			startCount++
		})
		d.Observe(func(ServiceEvent) { order = append(order, "done") })
		seed(ctx, fs, tree, name, request(id, "ping"))
		if err := d.RunOnce(ctx); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
		if startCount != 1 {
			t.Fatalf("ObserveStart fired %d times, want 1", startCount)
		}
		if len(order) != 2 || order[0] != "start" || order[1] != "done" {
			t.Errorf("event order = %v, want [start done]", order)
		}
		if got.Name != name || got.Req.ID != id || got.Req.Type != "ping" {
			t.Errorf("StartEvent = %+v, want name=%s id=%s type=ping", got, name, id)
		}
		return got
	}

	t.Run("drainNew", func(t *testing.T) {
		run(t, func(ctx context.Context, fs remotefs.FS, tree Tree, name string, data []byte) {
			if err := Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("recover", func(t *testing.T) {
		run(t, func(ctx context.Context, fs remotefs.FS, tree Tree, name string, data []byte) {
			// Stranded in cur/ (picked up but never answered before a crash).
			if err := fs.WriteFile(ctx, path.Join(tree.Outbox().Cur(), name), data); err != nil {
				t.Fatal(err)
			}
		})
	})

	t.Run("fires without a terminal observer", func(t *testing.T) {
		ctx, fs, tree, d := setup(t)
		id := NewID()
		name := RequestName(id)
		fired := false
		d.ObserveStart(func(StartEvent) { fired = true })
		if err := Deliver(ctx, fs, tree.Outbox(), name, request(id, "ping")); err != nil {
			t.Fatal(err)
		}
		if err := d.RunOnce(ctx); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
		if !fired {
			t.Error("ObserveStart did not fire when the terminal observer was nil")
		}
	})
}

func TestDispatch_RecoversStrandedRequest(t *testing.T) {
	ctx, fs, tree, d := setup(t)
	id := NewID()
	name := RequestName(id)
	// Simulate a crash mid-cycle: request picked up into cur/ but never answered.
	if err := fs.WriteFile(ctx, path.Join(tree.Outbox().Cur(), name), request(id, "ping")); err != nil {
		t.Fatal(err)
	}
	if err := d.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	resp, err := ReadResponse(ctx, fs, tree, name)
	if err != nil {
		t.Fatalf("stranded request was not recovered: %v", err)
	}
	if resp.Status != StatusOK {
		t.Errorf("recovered resp status = %q, want ok", resp.Status)
	}
}

func TestDispatch_UnknownType(t *testing.T) {
	ctx, fs, tree, d := setup(t)
	id := NewID()
	name := RequestName(id)
	if err := Deliver(ctx, fs, tree.Outbox(), name, request(id, "nope")); err != nil {
		t.Fatal(err)
	}
	if err := d.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	resp, err := ReadResponse(ctx, fs, tree, name)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != StatusError || resp.Error == nil || resp.Error.Code != "unknown_type" {
		t.Errorf("resp = %+v, want status error code unknown_type", resp)
	}
}

func TestEnvelope_Roundtrip(t *testing.T) {
	want := Request{
		SchemaVersion: SchemaVersion,
		ID:            "01ABC",
		Type:          "ping",
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
		Params:        json.RawMessage(`{"a":1}`),
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Request
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Type != want.Type || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("roundtrip mismatch: got %+v want %+v", got, want)
	}
}

func TestNewID_TimeSortable(t *testing.T) {
	a := NewID()
	time.Sleep(2 * time.Millisecond)
	b := NewID()
	if !(a < b) {
		t.Errorf("ULIDs not time-sortable: %q !< %q", a, b)
	}
	if len(a) != 26 {
		t.Errorf("ULID length = %d, want 26", len(a))
	}
}
