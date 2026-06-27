package skill

import (
	"strings"
	"testing"
)

// The doc is the contract — guard that it stays substantive and keeps mentioning
// the protocol's load-bearing pieces (and every verb).
func TestNanaMDCoversContracts(t *testing.T) {
	if len(NanaMD) < 1500 {
		t.Fatalf("NANA.md unexpectedly short: %d bytes", len(NanaMD))
	}
	for _, want := range []string{
		"schema_version", "outbox/new", "rename", "heartbeat", "absolute",
		"capabilities.json", "ping", "python.install", "pip.install", "web.fetch",
		"needs_clarification", "body_blob",
	} {
		if !strings.Contains(NanaMD, want) {
			t.Errorf("NANA.md missing key contract %q", want)
		}
	}
}
