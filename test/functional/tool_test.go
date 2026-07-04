//go:build functional

package functional

import (
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestInstallToolCatalog proves the data-driven tool catalog end to end on BOTH boxes: the
// embedded jq entry (github-release + shasums-file recipe) and fd (github-release + pinned)
// download, verify, land on the sandbox PATH (<root>/tools/bin), and RUN — on musl and glibc.
// `catalog list` needs no sandbox; the installs use the real relay/push path.
func TestInstallToolCatalog(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sb     func(*testing.T) sandboxConn
		bin    string
		verify string
	}{
		{"musl", requireSandbox, "jq", "jq --version"},
		{"glibc", requireGlibcSandbox, "fd", "fd --version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sb := tc.sb(t)
			root := "/tmp/iceclimber-tool-" + protocol.NewID()
			cfg := writeConfigRoot(t, sb, root)
			scheduleRootCleanupOn(t, sb.Name, root)
			runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

			out := string(runIceclimber(t, "install", "tool", tc.bin, "--config", cfg, "--transport", "sftp"))
			if !strings.Contains(out, "installed "+tc.bin) {
				t.Errorf("install tool %s output unexpected:\n%s", tc.bin, out)
			}
			// It landed on the tool PATH and actually runs on this box.
			run := limaShOn(t, sb.Name, root+"/tools/bin/"+tc.verify+" 2>&1 | head -1")
			if strings.TrimSpace(run) == "" || strings.Contains(run, "not found") {
				t.Errorf("%s did not run from tools/bin on %s: %q", tc.bin, tc.name, run)
			}
			// Idempotent.
			if out := string(runIceclimber(t, "install", "tool", tc.bin, "--config", cfg, "--transport", "sftp")); !strings.Contains(out, "already installed") {
				t.Errorf("second install tool %s should be idempotent:\n%s", tc.bin, out)
			}
			// Unknown tool → a clear error naming the available set.
			_, err := runIceclimberErr(t, "install", "tool", "definitelynotatool", "--config", cfg, "--transport", "sftp")
			if err == nil {
				t.Error("an unknown tool should error")
			}
		})
	}
}

// TestCatalogListNoSandbox: `catalog list` is controller-local — it works with no reachable
// box and shows the embedded entries.
func TestCatalogListNoSandbox(t *testing.T) {
	out := string(runIceclimber(t, "catalog", "list", "--config", writeConfigRoot(t, requireSandbox(t), "/tmp/unused")))
	for _, want := range []string{"bash", "jq", "fd"} {
		if !strings.Contains(out, want) {
			t.Errorf("catalog list missing %q:\n%s", want, out)
		}
	}
}

// TestInstallShellStillWorks: after bash migrated into the catalog, `install shell` must still
// install a runnable bash at <root>/shell/bash AND drop the rc (the shell-specific step).
func TestInstallShellStillWorks(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-shellmig-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)
	scheduleRootCleanupOn(t, sb.Name, root)
	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	out := string(runIceclimber(t, "install", "shell", "--config", cfg, "--transport", "sftp"))
	if !strings.Contains(out, "bash") || !strings.Contains(out, root+"/shell/bash") {
		t.Errorf("install shell output unexpected:\n%s", out)
	}
	if s := limaShOn(t, sb.Name, root+"/shell/bash --version 2>&1 | head -1"); !strings.Contains(s, "GNU bash") {
		t.Errorf("migrated bash did not run: %q", s)
	}
	if s := limaShOn(t, sb.Name, "test -f "+root+"/shell/bashrc && echo ok"); !strings.Contains(s, "ok") {
		t.Error("install shell must still drop the bashrc (the shell-specific step)")
	}
}
