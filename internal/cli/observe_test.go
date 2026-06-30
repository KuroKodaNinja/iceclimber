package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/probe"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

// TestIsOperatorDenied: only a gate operator_denied (handler never ran) is excluded
// from the serviced tally — an in-handler denial like egress_denied is a real serviced
// outcome and still counts.
func TestIsOperatorDenied(t *testing.T) {
	if !isOperatorDenied(protocol.Errf("r", protocol.CodeOperatorDenied, "no")) {
		t.Error("operator_denied should be excluded from serviced")
	}
	if isOperatorDenied(protocol.OK("r", map[string]string{})) {
		t.Error("an ok response is serviced")
	}
	if isOperatorDenied(protocol.Errf("r", "egress_denied", "no")) {
		t.Error("an in-handler egress_denied is a serviced outcome, not a gate denial")
	}
}

// TestServicedEvent: both observers (console + headless serve) build their activity
// event through this one helper, so it must (a) carry id/type/status/duration for a
// completed request, (b) skip a gate-denied request so it counts as a denial only, and
// (c) still count an in-handler denial (egress_denied) as a serviced outcome. Without
// this, a regression deleting the gate-deny skip would inflate the serviced tally and
// pass every other test.
func TestServicedEvent(t *testing.T) {
	ev := protocol.ServiceEvent{
		Req:  protocol.Request{Type: "pip.install"},
		Resp: protocol.OK("req-1", map[string]any{}),
		Dur:  3200 * time.Millisecond,
	}
	e, ok := servicedEvent(ev)
	if !ok {
		t.Fatal("an ok response should produce a serviced event")
	}
	if e.Kind != activity.KindServiced || e.Type != "pip.install" || e.ID != "req-1" {
		t.Errorf("unexpected serviced event: %+v", e)
	}
	if e.Status != protocol.StatusOK {
		t.Errorf("status = %q, want %q", e.Status, protocol.StatusOK)
	}
	if e.DurMS != 3200 {
		t.Errorf("DurMS = %d, want 3200", e.DurMS)
	}

	denied := protocol.ServiceEvent{
		Req:  protocol.Request{Type: "web.fetch"},
		Resp: protocol.Errf("req-2", protocol.CodeOperatorDenied, "denied by operator"),
	}
	if _, ok := servicedEvent(denied); ok {
		t.Error("an operator-denied request must not produce a serviced event")
	}

	egress := protocol.ServiceEvent{
		Req:  protocol.Request{Type: "web.fetch"},
		Resp: protocol.Errf("req-3", "egress_denied", "host not allowed"),
	}
	if _, ok := servicedEvent(egress); !ok {
		t.Error("an in-handler egress_denied is a serviced outcome and must count")
	}
}

// TestConsoleDispatcher_StartedIsEphemeral: the console pushes a live KindStarted
// event the moment a request is picked up (so the operator sees it in-progress), but
// the durable JSONL must hold ONLY the serviced line — a started line would double the
// log and corrupt seed-on-restart counters.
func TestConsoleDispatcher_StartedIsEphemeral(t *testing.T) {
	ctx := context.Background()
	fs := remotefs.NewExecFS(remotefstest.LocalRunner{})
	tree := protocol.Tree{Root: t.TempDir()}
	if err := protocol.EnsureTree(ctx, fs, tree); err != nil {
		t.Fatalf("EnsureTree: %v", err)
	}
	sess := &session{fs: fs, tree: tree, transport: "exec", sandboxID: "s", fp: &probe.Fingerprint{}}
	logPath := filepath.Join(t.TempDir(), "activity.jsonl")
	act := activity.New(logPath)
	events := make(chan tea.Msg, 16)
	disp := buildConsoleDispatcher(ctx, sess, &config.Config{SandboxID: "s"}, act, events)

	id := protocol.NewID()
	name := protocol.RequestName(id)
	data, _ := json.Marshal(protocol.Request{
		SchemaVersion: protocol.SchemaVersion, ID: id, Type: "ping",
		CreatedAt: time.Now().UTC(), Params: json.RawMessage("{}"),
	})
	if err := protocol.Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
		t.Fatal(err)
	}
	if err := disp.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Live channel: both a started and a serviced event reached the UI.
	var sawStarted, sawServiced bool
	for drain := true; drain; {
		select {
		case m := <-events:
			if e, ok := m.(activity.Event); ok {
				switch e.Kind {
				case activity.KindStarted:
					sawStarted = true
				case activity.KindServiced:
					sawServiced = true
				}
			}
		default:
			drain = false
		}
	}
	if !sawStarted || !sawServiced {
		t.Errorf("live events: started=%v serviced=%v, want both", sawStarted, sawServiced)
	}

	// Durable log: exactly the serviced line, never the started one.
	evs, err := activity.Read(logPath)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, e := range evs {
		if e.Kind == activity.KindStarted {
			t.Errorf("durable log contains a started line (must be ephemeral): %+v", e)
		}
	}
	if s, _, _ := activity.Counts(evs); s != 1 {
		t.Errorf("durable serviced count = %d, want 1", s)
	}
}
