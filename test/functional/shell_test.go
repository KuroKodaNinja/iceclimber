//go:build functional

package functional

import (
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestInstallShellAndShellCommand proves the operator shell ergonomics end to end on the
// musl/BusyBox box: `iceclimber install shell` relays a static bash in (over both transports)
// and it actually runs; then `iceclimber shell -c` opens a (non-interactive) session with the
// iceclimber environment sourced — ICECLIMBER_HOME set, and popo + the installed bash resolvable
// by name on PATH (the "reasonable path/environment" the feature promises).
func TestInstallShellAndShellCommand(t *testing.T) {
	sb := requireSandbox(t)

	for _, transport := range []string{"sftp", "exec"} {
		t.Run(transport, func(t *testing.T) {
			root := "/tmp/iceclimber-shell-" + protocol.NewID()
			cfg := writeConfigRoot(t, sb, root)
			scheduleRootCleanup(t, root)
			runIceclimber(t, "bootstrap", "--config", cfg, "--transport", transport)

			out := string(runIceclimber(t, "install", "shell", "--config", cfg, "--transport", transport))
			if !strings.Contains(out, "bash") || !strings.Contains(out, root+"/shell/bash") {
				t.Errorf("install shell output unexpected:\n%s", out)
			}
			// The relayed bash is present, executable, and runs on the box.
			if s := limaSh(t, root+"/shell/bash --version 2>&1 | head -1"); !strings.Contains(s, "GNU bash") {
				t.Errorf("installed bash did not run on the sandbox: %q", s)
			}
			// The rc file was dropped alongside it.
			if s := limaSh(t, "test -f "+root+"/shell/bashrc && echo ok || echo no"); !strings.Contains(s, "ok") {
				t.Errorf("bashrc not dropped at %s/shell/bashrc", root)
			}

			// Re-install is idempotent (already installed).
			if out := string(runIceclimber(t, "install", "shell", "--config", cfg, "--transport", transport)); !strings.Contains(out, "already installed") {
				t.Errorf("second install shell should be idempotent:\n%s", out)
			}

			// `iceclimber shell -c` runs a command in the sourced sandbox environment (no PTY).
			cout := string(runIceclimber(t, "shell", "-c",
				"echo HOME=$ICECLIMBER_HOME; echo BASH=$(command -v bash); echo POPO=$(command -v popo)",
				"--config", cfg, "--transport", transport))
			if !strings.Contains(cout, "HOME="+root) {
				t.Errorf("shell -c did not set ICECLIMBER_HOME:\n%s", cout)
			}
			// bash is on PATH — resolved either at <root>/shell/bash or its tools/bin link
			// (bash migrated into the tool catalog, which also links it into tools/bin; that
			// dir precedes shell/ on PATH, so `command -v` may report the link — both are bash).
			if !strings.Contains(cout, "BASH="+root+"/shell/bash") && !strings.Contains(cout, "BASH="+root+"/tools/bin/bash") {
				t.Errorf("installed bash not on PATH in shell -c:\n%s", cout)
			}
			if !strings.Contains(cout, "POPO="+root+"/popo") {
				t.Errorf("popo not on PATH in shell -c:\n%s", cout)
			}
		})
	}
}
