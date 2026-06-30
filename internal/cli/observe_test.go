package cli

import (
	"testing"

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
