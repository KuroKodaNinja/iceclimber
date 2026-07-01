package skill

import (
	"strings"
	"testing"
)

// NANA.md is the system-prompt contract: it stays FOCUSED (it's injected into the agent's
// system prompt) and defaults to the `popo` client, with the file-protocol fallback pointer.
// The heavy raw mechanics live in PROTOCOL.md, not here — but load-bearing behavioral guidance
// (e.g. defaulting runtime versions to the latest) does belong, so the bound is generous.
func TestNanaMDIsMinimalAndPointsToPopo(t *testing.T) {
	if len(NanaMD) > 4500 {
		t.Errorf("NANA.md should stay focused (system prompt); got %d bytes", len(NanaMD))
	}
	for _, want := range []string{"popo", "popo help", "absolute path", "PROTOCOL.md"} {
		if !strings.Contains(NanaMD, want) {
			t.Errorf("NANA.md missing %q", want)
		}
	}
	// Runtime-version guidance must be present: default to the latest, don't guess a number
	// (the whole point of the section — an agent left to guess picks an older line).
	for _, want := range []string{"node.install", "latest"} {
		if !strings.Contains(NanaMD, want) {
			t.Errorf("NANA.md missing runtime-version guidance %q", want)
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
