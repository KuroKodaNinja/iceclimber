//go:build functional

package functional

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestPipRelay exercises Tier 1 (Popo-side fetch + relay) with NO mirror
// configured: Popo's own python downloads sandbox-platform wheels and relays
// them in for an offline install. The CLI case uses a C-extension (markupsafe)
// to prove a musl wheel built off-box on the controller actually runs on Alpine.
func TestPipRelay(t *testing.T) {
	if !controllerHasPip() {
		t.Skip("Tier 1 relay needs python3 + pip on the controller")
	}
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-relay-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root) // no pip mirror -> auto resolves to relay

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")
	runIceclimber(t, "install", "python", "3.12", "--config", cfg, "--transport", "sftp")

	// CLI relay path with a C-extension package.
	out := runIceclimber(t, "install", "pip", "markupsafe==2.1.5",
		"--config", cfg, "--transport", "sftp", "--python", "3.12", "--tier", "relay")
	if !strings.Contains(string(out), "(relay)") {
		t.Errorf("expected a relay-tier install in output:\n%s", out)
	}
	imp := fmt.Sprintf(`for d in %s/runtimes/python/*/bin/python3; do "$d" -c 'import markupsafe; print(markupsafe.__version__)'; done`, root)
	if got := limaSh(t, imp); !strings.Contains(got, "2.1.5") {
		t.Errorf("import markupsafe (C-ext musl wheel) in sandbox = %q", got)
	}

	// Verb path: auto tier with no mirror → relay.
	fs, cleanup := dialFS(t, sb, "sftp")
	defer cleanup()
	resp := pipInstallViaServe(t, context.Background(), fs, protocol.Tree{Root: root},
		cfg, `{"python_version":"3.12","packages":[{"name":"six","version":"1.16.0"}]}`)
	if resp.Status != protocol.StatusOK {
		t.Fatalf("pip.install status = %q, error = %+v", resp.Status, resp.Error)
	}
	var oc pkg.Outcome
	if err := json.Unmarshal(resp.Result, &oc); err != nil {
		t.Fatalf("unmarshal outcome: %v", err)
	}
	if len(oc.Installed) == 0 || oc.Installed[0].Tier != pkg.TierRelay {
		t.Errorf("verb outcome = %+v, want tier relay", oc)
	}
}

func controllerHasPip() bool {
	if _, err := exec.LookPath("python3"); err != nil {
		return false
	}
	return exec.Command("python3", "-m", "pip", "--version").Run() == nil
}
