package skill

import (
	"strings"
	"testing"
)

// NANA.md is the system-prompt contract: it must stay MINIMAL (it's injected into the
// agent's system prompt) and default to the `popo` client, with the file-protocol
// fallback pointer. The heavy mechanics live in PROTOCOL.md, not here.
func TestNanaMDIsMinimalAndPointsToPopo(t *testing.T) {
	if len(NanaMD) > 2500 {
		t.Errorf("NANA.md is meant to be minimal (system prompt); got %d bytes", len(NanaMD))
	}
	for _, want := range []string{"popo", "popo help", "absolute path", "PROTOCOL.md"} {
		if !strings.Contains(NanaMD, want) {
			t.Errorf("NANA.md missing %q", want)
		}
	}
	// The raw mechanics must NOT be in the system prompt — they belong in PROTOCOL.md.
	for _, leaked := range []string{"schema_version", "outbox/new", "rename"} {
		if strings.Contains(NanaMD, leaked) {
			t.Errorf("NANA.md leaks raw-protocol detail %q (belongs in PROTOCOL.md)", leaked)
		}
	}
}

// PROTOCOL.md is the raw file-protocol reference (no-exec fallback): guard that it
// keeps the load-bearing pieces and every verb.
func TestProtocolMDCoversContracts(t *testing.T) {
	if len(ProtocolMD) < 1500 {
		t.Fatalf("PROTOCOL.md unexpectedly short: %d bytes", len(ProtocolMD))
	}
	for _, want := range []string{
		"schema_version", "outbox/new", "rename", "heartbeat", "absolute",
		"capabilities.json", "ping", "python.install", "pip.install", "web.fetch",
		"needs_clarification", "body_blob",
	} {
		if !strings.Contains(ProtocolMD, want) {
			t.Errorf("PROTOCOL.md missing key contract %q", want)
		}
	}
}
