package skill

import (
	"strings"
	"testing"
)

// NANA.md is the system-prompt contract: it stays FOCUSED (it's injected into the agent's
// system prompt) and defaults to the `popo` client, with the file-protocol fallback pointer.
// The heavy raw mechanics live in PROTOCOL.md, not here — but load-bearing behavioral guidance
// (e.g. defaulting runtime versions to the latest) does belong, so the bound is generous.
// Both egress-mode renders are held to the same contract.
func TestNanaMDIsMinimalAndPointsToPopo(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proxy bool
	}{{"relay", false}, {"proxy", true}} {
		t.Run(tc.name, func(t *testing.T) {
			doc := NanaMD(tc.proxy)
			// A focus ceiling, not a hard limit — raised as real capabilities land (runtime
			// version guidance #83, the tool catalog #96). The bar is "does every line earn
			// its place in the system prompt", checked in review; this catches unbounded drift.
			if len(doc) > 5500 {
				t.Errorf("NANA.md (%s) should stay focused (system prompt); got %d bytes", tc.name, len(doc))
			}
			// The mode marker must be fully spliced out — a leftover placeholder means the
			// agent got a doc with a hole where its network contract should be.
			if strings.Contains(doc, networkMarker) {
				t.Errorf("NANA.md (%s) still contains the unspliced %q marker", tc.name, networkMarker)
			}
			for _, want := range []string{"popo", "popo help", "absolute path", "PROTOCOL.md"} {
				if !strings.Contains(doc, want) {
					t.Errorf("NANA.md (%s) missing %q", tc.name, want)
				}
			}
			// Behavioral guardrails that keep an air-gapped agent doing the right thing must
			// stay in the system prompt — each pins a real failure mode observed/anticipated
			// in use. They hold in BOTH egress modes:
			for _, want := range []string{
				"node.install",              // the omit-version install form (agent stops guessing)
				"latest",                    // … resolves to the recommended line
				"default to the latest",     // version guidance is explicit, not implied
				"Reuse the version",         // use the installed runtime version for package installs
				"Install the runtime first", // runtime before its packages
				"no fallback",               // never route around Popo when it's slow/down
				"be patient",                // don't cancel/retry a slow (minutes-long) request
			} {
				if !strings.Contains(doc, want) {
					t.Errorf("NANA.md (%s) missing behavioral guardrail %q", tc.name, want)
				}
			}
			// The raw mechanics must NOT be in the system prompt — they belong in PROTOCOL.md.
			for _, leaked := range []string{"schema_version", "outbox/new", "rename"} {
				if strings.Contains(doc, leaked) {
					t.Errorf("NANA.md (%s) leaks raw-protocol detail %q (belongs in PROTOCOL.md)", tc.name, leaked)
				}
			}
		})
	}
}

// The network-access section is the one mode-dependent piece: relay categorically forbids
// native network tools; proxy (the default) documents that they WORK through the gated proxy —
// with runtimes still popo-only and new hosts held for approval. A doc that forbids what the
// mode enables would fight the agent (the pre-render bug this test pins).
func TestNanaMDNetworkSectionMatchesEgressMode(t *testing.T) {
	relay := NanaMD(false)
	proxy := NanaMD(true)

	// Relay: the categorical prohibition, and NO gated-proxy talk.
	if !strings.Contains(relay, "Never") || !strings.Contains(relay, "no internet") {
		t.Errorf("relay NANA.md missing the categorical no-network prohibition")
	}
	if strings.Contains(relay, "gated proxy") || strings.Contains(relay, "HTTPS_PROXY") {
		t.Errorf("relay NANA.md must not describe proxy egress")
	}

	// Proxy: native tools are documented as working through the proxy…
	for _, want := range []string{"gated proxy", "HTTPS_PROXY", "npm ci"} {
		if !strings.Contains(proxy, want) {
			t.Errorf("proxy NANA.md missing %q", want)
		}
	}
	// …with the two safety rules: runtimes stay popo-only, and new hosts may be held.
	if !strings.Contains(proxy, "popo <lang>.install") {
		t.Errorf("proxy NANA.md must keep runtimes popo-only")
	}
	if !strings.Contains(proxy, "operator approval") {
		t.Errorf("proxy NANA.md must warn that new hosts are held for approval")
	}
	// …and it must NOT carry relay's categorical "never use pip/npm/git" prohibition.
	if strings.Contains(proxy, "**Never** reach the network") {
		t.Errorf("proxy NANA.md still forbids the native tools the proxy exists to enable")
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
