//go:build functional

package functional

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestEgressGating exercises the §6.1 policy on the VM: rewrite re-venue (ungated),
// the hold → approve → controller-fetch flow, the SSRF floor, and deny. The VM
// has open egress, so this tests the gating/venue *logic*, not a network boundary.
func TestEgressGating(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-egress-" + protocol.NewID()
	dir := t.TempDir()
	approvals := filepath.Join(dir, "approvals.json")
	audit := filepath.Join(dir, "audit.jsonl")
	cfg := writeConfigEgress(t, sb, root, approvals, audit)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	icx := func(args ...string) (string, error) {
		out, err := exec.Command(iceclimberBin, append(args, "--config", cfg)...).CombinedOutput()
		return string(out), err
	}

	// 1. Rewrite re-venues example.org → example.com over the sandbox (ungated).
	if out, err := icx("web", "fetch", "https://example.org", "--transport", "sftp"); err != nil || !strings.Contains(out, "sandbox-exec") {
		t.Errorf("rewrite fetch: %v\n%s", err, out)
	}
	if a, _ := os.ReadFile(audit); !strings.Contains(string(a), `"rewritten_url":"https://example.com`) {
		t.Errorf("audit missing rewritten_url:\n%s", a)
	}

	// 2. Unlisted → controller → held.
	out, _ := icx("web", "fetch", "https://example.com", "--transport", "sftp")
	if !strings.Contains(out, "requires approval") {
		t.Fatalf("expected hold, got:\n%s", out)
	}
	id := pendingID(t, icx, "example.com")

	// 3. Approve → re-submit → fetched via controller.
	if _, err := icx("approve", id); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if out, err := icx("web", "fetch", "https://example.com", "--transport", "sftp"); err != nil || !strings.Contains(out, "venue controller") {
		t.Errorf("post-approve fetch: %v\n%s", err, out)
	}

	// 4. SSRF floor: a literal metadata address is refused up front.
	if out, err := icx("web", "fetch", "http://169.254.169.254/", "--transport", "sftp"); err == nil {
		t.Errorf("expected SSRF refusal, got:\n%s", out)
	}

	// 5. Deny → re-submit gets egress_denied.
	icx("web", "fetch", "https://blocked.example.net", "--transport", "sftp")
	did := pendingID(t, icx, "blocked.example.net")
	if _, err := icx("deny", did, "--reason", "test"); err != nil {
		t.Fatalf("deny: %v", err)
	}
	if out, err := icx("web", "fetch", "https://blocked.example.net", "--transport", "sftp"); err == nil || !strings.Contains(out, "denied") {
		t.Errorf("expected egress denied, got: %v\n%s", err, out)
	}
}

func pendingID(t *testing.T, icx func(...string) (string, error), urlSubstr string) string {
	t.Helper()
	out, err := icx("pending")
	if err != nil {
		t.Fatalf("pending: %v\n%s", err, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, urlSubstr) {
			return strings.Fields(line)[0]
		}
	}
	t.Fatalf("no pending entry for %q in:\n%s", urlSubstr, out)
	return ""
}

func writeConfigEgress(t *testing.T, sb sandboxConn, root, approvals, audit string) string {
	t.Helper()
	content := fmt.Sprintf(`sandbox_id: %s
ssh:
  host: %s
  port: %d
  user: %s
  identity_file: %s
  known_hosts: %s
remote_root: %s
approvals_file: %s
audit_log: %s
fetch_rewrites:
  - match: "https://example.org/*"
    rewrite_to: "https://example.com/*"
    venue: sandbox
`, sandboxName, sb.Host, sb.Port, sb.User, sb.IdentityFile, sb.KnownHosts, root, approvals, audit)
	p := filepath.Join(t.TempDir(), "iceclimber.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	scheduleRootCleanup(t, root)
	return p
}
