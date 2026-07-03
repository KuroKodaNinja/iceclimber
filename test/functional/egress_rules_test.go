//go:build functional

package functional

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The `egress` group operates on controller-local stores (approvals/pending JSON) — no
// SSH session — so this drives the real binary against a fixture store, like the views
// tests. It covers the CLI-parity gap the console's egress panel exposed: rules were
// previously manageable only from the TUI.
func TestEgressRulesCLI(t *testing.T) {
	dir := t.TempDir()
	approvals := filepath.Join(dir, "approvals.json")
	cfgPath := filepath.Join(dir, "iceclimber.yaml")
	cfg := fmt.Sprintf(`sandbox_id: rulestest
ssh:
  host: 127.0.0.1
  user: nobody
remote_root: /tmp/rulestest
approvals_file: %s
`, approvals)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(approvals, []byte(`{"allow":["https://xkcd.com/*","dual.example"],"deny":["https://evil.example/*","dual.example"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(wantErr bool, args ...string) string {
		t.Helper()
		out, err := exec.Command(iceclimberBin, append(args, "--config", cfgPath)...).CombinedOutput()
		if (err != nil) != wantErr {
			t.Fatalf("%v: err=%v\n%s", args, err, out)
		}
		return string(out)
	}

	// list shows both kinds.
	out := run(false, "egress", "rules", "list")
	for _, want := range []string{"allow  https://xkcd.com/*", "deny   https://evil.example/*"} {
		if !strings.Contains(out, want) {
			t.Errorf("rules list missing %q:\n%s", want, out)
		}
	}
	// --json is machine-readable.
	var parsed struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	}
	if err := json.Unmarshal([]byte(run(false, "egress", "rules", "list", "--json")), &parsed); err != nil {
		t.Fatalf("rules list --json not parseable: %v", err)
	}
	if len(parsed.Allow) != 2 || len(parsed.Deny) != 2 {
		t.Errorf("json rules = %+v, want 2 allow / 2 deny", parsed)
	}

	// forget removes an unambiguous rule…
	run(false, "egress", "rules", "forget", "https://evil.example/*")
	if out := run(false, "egress", "rules", "list"); strings.Contains(out, "evil.example") {
		t.Errorf("deny rule not forgotten:\n%s", out)
	}
	// …demands --kind for an ambiguous one…
	if out := run(true, "egress", "rules", "forget", "dual.example"); !strings.Contains(out, "--kind") {
		t.Errorf("ambiguous forget should demand --kind:\n%s", out)
	}
	run(false, "egress", "rules", "forget", "dual.example", "--kind", "deny")
	// …and errors clearly on an unknown pattern.
	if out := run(true, "egress", "rules", "forget", "nope.example"); !strings.Contains(out, "rules list") {
		t.Errorf("unknown forget should point at rules list:\n%s", out)
	}

	// The hidden top-level alias still answers (script compat).
	if out := run(false, "pending"); !strings.Contains(out, "no pending requests") {
		t.Errorf("hidden alias `pending` broken:\n%s", out)
	}
	// And the grouped form matches.
	if out := run(false, "egress", "pending"); !strings.Contains(out, "no pending requests") {
		t.Errorf("egress pending broken:\n%s", out)
	}
}
