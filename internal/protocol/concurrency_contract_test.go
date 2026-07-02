package protocol

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/skill"
)

// TestPingDeclaresConcurrency (K1): Popo declares its dispatch concurrency limit in the ping
// result, so the agent discovers N deterministically instead of assuming it (the runtime half
// of the concurrency contract; PROTOCOL.md documents the field).
func TestPingDeclaresConcurrency(t *testing.T) {
	resp := PingHandler("9.9.9-test", 7)(t.Context(), Request{ID: "p1", Type: "ping"})
	if resp.Status != StatusOK {
		t.Fatalf("ping status = %q, want ok", resp.Status)
	}
	var got struct {
		DispatchConcurrency int `json:"dispatch_concurrency"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode ping result: %v", err)
	}
	if got.DispatchConcurrency != 7 {
		t.Errorf("ping dispatch_concurrency = %d, want 7", got.DispatchConcurrency)
	}
}

// TestConcurrencyContractMatchesDoc (K2): the same-runtime grouping Popo ENFORCES (runtimeFamily,
// which drives the keyed serialization in drainNew) must match what PROTOCOL.md's "Concurrency"
// section DOCUMENTS — so the contract Popo states and the behavior it enforces can't drift.
func TestConcurrencyContractMatchesDoc(t *testing.T) {
	// The install verbs that share a runtime line (must be serialized) — each maps to a family.
	grouped := []string{
		"python.install", "pip.install",
		"node.install", "npm.install",
		"java.install", "maven.install", "maven.build",
		"conda.install",
	}
	// Verbs with no shared target — always parallel (empty family).
	free := []string{"ping", "web.fetch", "collect"}

	for _, v := range grouped {
		if runtimeFamily(v) == "" {
			t.Errorf("runtimeFamily(%q) is empty, but it shares a runtime line (must serialize)", v)
		}
	}
	for _, v := range free {
		if runtimeFamily(v) != "" {
			t.Errorf("runtimeFamily(%q) = %q, want empty (a quick verb must stay fully parallel)", v, runtimeFamily(v))
		}
	}

	// The doc's Concurrency section must name every grouped verb, so an agent reading PROTOCOL.md
	// sees the same grouping Popo enforces.
	section := concurrencySection(t, skill.ProtocolMD)
	for _, v := range grouped {
		if !strings.Contains(section, v) {
			t.Errorf("PROTOCOL.md Concurrency section omits %q (documented grouping must match runtimeFamily)", v)
		}
	}
}

// concurrencySection returns the text of PROTOCOL.md's "## Concurrency" section.
func concurrencySection(t *testing.T, doc string) string {
	t.Helper()
	i := strings.Index(doc, "## Concurrency")
	if i < 0 {
		t.Fatal("PROTOCOL.md has no ## Concurrency section")
	}
	rest := doc[i+len("## Concurrency"):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		return rest[:j]
	}
	return rest
}
