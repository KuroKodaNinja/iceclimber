//go:build functional

package functional

import (
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestCleanBox_UnprovisionedThenBootstrap covers the sandbox lifecycle from a genuinely
// clean remote_root — the case every other functional test skips by bootstrapping first.
// It pins the regression that connecting to an un-bootstrapped box must fail fast with a
// clear message (not a raw "list outbox" error, and never a reconnect/prompt storm):
//
//  1. status on a fresh root degrades gracefully (no tree, no crash);
//  2. serve --once refuses with the "not bootstrapped — run `iceclimber bootstrap`" error;
//  3. after bootstrap, status/serve/install all work (the separated flow end to end).
func TestCleanBox_UnprovisionedThenBootstrap(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-clean-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)

	// (1) A fresh root has no protocol tree. status must still run — it reads best-effort
	// and reports the box as empty rather than erroring on the missing dirs.
	statusOut := string(runIceclimber(t, "status", "--config", cfg, "--transport", "sftp"))
	if !strings.Contains(statusOut, "none yet") || !strings.Contains(statusOut, "none installed") {
		t.Errorf("clean-box status should degrade gracefully (heartbeat none yet / runtimes none installed):\n%s", statusOut)
	}

	// (2) serve --once against an un-bootstrapped box must fail fast with the actionable
	// message — NOT a raw maildir "list outbox" error, and without looping or prompting.
	serveOut, err := runIceclimberErr(t, "serve", "--once", "--config", cfg, "--transport", "sftp")
	if err == nil {
		t.Fatalf("serve --once on a clean box should fail (not bootstrapped), got success:\n%s", serveOut)
	}
	if !strings.Contains(serveOut, "not bootstrapped") || !strings.Contains(serveOut, "iceclimber bootstrap") {
		t.Errorf("serve --once error must name the fix (run `iceclimber bootstrap`); got:\n%s", serveOut)
	}
	if strings.Contains(serveOut, "list outbox") || strings.Contains(serveOut, "no such file") {
		t.Errorf("serve --once leaked a raw maildir error instead of the not-bootstrapped message:\n%s", serveOut)
	}

	// (3) Bootstrap, then the same operations succeed — proving the gate is exactly the
	// missing tree and nothing structural.
	bootOut := string(runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp"))
	if !strings.Contains(bootOut, "smoke test") {
		t.Errorf("bootstrap output lacks smoke-test confirmation:\n%s", bootOut)
	}
	// serve --once now runs a clean dispatch cycle (empty outbox → nothing to do → success).
	if out, err := runIceclimberErr(t, "serve", "--once", "--config", cfg, "--transport", "sftp"); err != nil {
		t.Fatalf("serve --once after bootstrap should succeed: %v\n%s", err, out)
	}
	// install is a separate, post-bootstrap concern (bootstrap no longer touches runtimes).
	// A managed CPython proves the split flow works end to end on the clean box.
	installOut := string(runIceclimber(t, "install", "python", "3.12", "--config", cfg, "--transport", "sftp"))
	if !strings.Contains(installOut, "python 3.12.") || !strings.Contains(installOut, "/runtimes/python/") {
		t.Errorf("install python should report the managed runtime:\n%s", installOut)
	}
	// status now reflects the provisioned box (a heartbeat exists after serve).
	statusOut = string(runIceclimber(t, "status", "--config", cfg, "--transport", "sftp"))
	if strings.Contains(statusOut, "none yet") {
		t.Errorf("after bootstrap+serve, status should show a heartbeat, not `none yet`:\n%s", statusOut)
	}
}
