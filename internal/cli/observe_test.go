package cli

import (
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
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
