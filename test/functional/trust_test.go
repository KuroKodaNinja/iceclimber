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

// TestTrustRecordsHostKey proves `iceclimber trust` records the sandbox's host key
// into a fresh known_hosts so a subsequent connect succeeds — the in-CLI
// replacement for the out-of-band ssh-keyscan that ephemeral sandboxes make
// painful. It also checks the unknown-host failure beforehand and idempotency after.
func TestTrustRecordsHostKey(t *testing.T) {
	sb := requireSandbox(t)
	kh := filepath.Join(t.TempDir(), "known_hosts") // intentionally does not exist yet
	root := "/tmp/iceclimber-trust-" + protocol.NewID()
	cfg := writeConfigKnownHosts(t, sb, root, kh)

	// Before trust: the host is unknown, so a connect must fail with a host-key error.
	if out, err := exec.Command(iceclimberBin, "probe", "--config", cfg).CombinedOutput(); err == nil {
		t.Fatalf("probe succeeded before trust; want host-key failure:\n%s", out)
	} else if !strings.Contains(strings.ToLower(string(out)), "host key") {
		t.Errorf("probe error did not mention the host key:\n%s", out)
	}

	// trust --yes fetches the key, shows its fingerprint, and records it.
	out := string(runIceclimber(t, "trust", "--yes", "--config", cfg))
	if !strings.Contains(out, "SHA256:") || !strings.Contains(out, "recorded host key") {
		t.Errorf("trust output unexpected:\n%s", out)
	}
	if _, err := os.Stat(kh); err != nil {
		t.Fatalf("known_hosts not created at %s: %v", kh, err)
	}

	// After trust: a connect succeeds.
	if got := string(runIceclimber(t, "probe", "--config", cfg)); !strings.Contains(got, "os/arch:") {
		t.Errorf("probe after trust did not connect:\n%s", got)
	}

	// Idempotent: trusting again recognises the recorded key.
	if got := string(runIceclimber(t, "trust", "--yes", "--config", cfg)); !strings.Contains(got, "already trusted") {
		t.Errorf("second trust was not idempotent:\n%s", got)
	}
}

// TestTrustFingerprintMismatch proves --fingerprint refuses to record a key whose
// fingerprint does not match the operator-supplied value (the safe automation path).
func TestTrustFingerprintMismatch(t *testing.T) {
	sb := requireSandbox(t)
	kh := filepath.Join(t.TempDir(), "known_hosts")
	root := "/tmp/iceclimber-trustfp-" + protocol.NewID()
	cfg := writeConfigKnownHosts(t, sb, root, kh)

	out, err := exec.Command(iceclimberBin, "trust", "--fingerprint", "SHA256:deadbeefnope", "--config", cfg).CombinedOutput()
	if err == nil {
		t.Fatalf("trust with a wrong --fingerprint succeeded; want failure:\n%s", out)
	}
	if !strings.Contains(string(out), "does not match") {
		t.Errorf("mismatch error unexpected:\n%s", out)
	}
	if _, statErr := os.Stat(kh); statErr == nil {
		t.Error("known_hosts was written despite a fingerprint mismatch")
	}
}

// writeConfigKnownHosts writes a config pointing known_hosts at a specific path
// (used to exercise trust from a clean slate).
func writeConfigKnownHosts(t *testing.T, sb sandboxConn, root, knownHosts string) string {
	t.Helper()
	content := fmt.Sprintf(`sandbox_id: %s
ssh:
  host: %s
  port: %d
  user: %s
  identity_file: %s
  known_hosts: %s
remote_root: %s
`, sandboxName, sb.Host, sb.Port, sb.User, sb.IdentityFile, knownHosts, root)
	path := filepath.Join(t.TempDir(), "iceclimber.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	scheduleRootCleanup(t, root)
	return path
}
