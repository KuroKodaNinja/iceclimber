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

// TestNpmInstallTier0 installs Node, then an npm package via Tier 0 (the sandbox
// reaches the registry), and proves the agent can require() it by running node
// with the returned NODE_PATH. Pure-JS package, no native addons.
func TestNpmInstallTier0(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-npm0-" + protocol.NewID()
	cfg := writeNpmConfig(t, sb, root, "https://registry.npmjs.org")

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")
	runIceclimber(t, "install", "node", "24", "--config", cfg, "--transport", "sftp")

	out := string(runIceclimber(t, "install", "npm", "left-pad", "--node", "24", "--config", cfg, "--transport", "sftp"))
	if !strings.Contains(out, "installed left-pad") {
		t.Fatalf("npm install (Tier 0) output:\n%s", out)
	}
	nodePath := grepNodePath(t, out)

	// The agent's usage: NODE_PATH=<...> node -e "require('left-pad')(...)".
	nodeDir := strings.TrimSuffix(nodePath, "/lib/node_modules")
	nodeBin := nodeDir + "/bin/node"
	script := `console.log(require('left-pad')('x', 5))`
	cmd := fmt.Sprintf("NODE_PATH=%s %s -e %s", remoteQuote(nodePath), remoteQuote(nodeBin), remoteQuote(script))
	res := limaSh(t, cmd)
	if !strings.Contains(res, "    x") { // left-pad('x',5) computes 4 spaces + x
		t.Errorf("require('left-pad') output = %q, want it to contain %q", res, "    x")
	}
}

// TestNpmInstallTier1Relay forces the relay: the controller's npm installs into a
// staging prefix and Popo relays the node_modules tree in — the sandbox runs no
// npm and needs no registry. Then the package require()s via NODE_PATH.
func TestNpmInstallTier1Relay(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("Tier 1 relay needs npm on the controller (this host)")
	}
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-npm1-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root) // no registry_url

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")
	runIceclimber(t, "install", "node", "24", "--config", cfg, "--transport", "sftp")

	out := string(runIceclimber(t, "install", "npm", "left-pad", "--node", "24", "--tier", "relay", "--config", cfg, "--transport", "sftp"))
	if !strings.Contains(out, "installed left-pad") || !strings.Contains(out, "(relay)") {
		t.Fatalf("npm relay install output:\n%s", out)
	}
	nodePath := grepNodePath(t, out)

	nodeBin := strings.TrimSuffix(nodePath, "/lib/node_modules") + "/bin/node"
	cmd := fmt.Sprintf("NODE_PATH=%s %s -e %s", remoteQuote(nodePath), remoteQuote(nodeBin), remoteQuote(`console.log(require('left-pad')('y', 4))`))
	if res := limaSh(t, cmd); !strings.Contains(res, "   y") { // left-pad('y',4) → 3 spaces + y
		t.Errorf("relay require('left-pad') = %q, want it to contain %q", res, "   y")
	}
}

func writeNpmConfig(t *testing.T, sb sandboxConn, root, registry string) string {
	t.Helper()
	content := fmt.Sprintf(`sandbox_id: %s
ssh:
  host: %s
  port: %d
  user: %s
  identity_file: %s
  known_hosts: %s
remote_root: %s
npm:
  registry_url: %s
`, sandboxName, sb.Host, sb.Port, sb.User, sb.IdentityFile, sb.KnownHosts, root, registry)
	path := filepath.Join(t.TempDir(), "iceclimber.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	scheduleRootCleanup(t, root)
	return path
}

func grepNodePath(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "NODE_PATH="); i >= 0 {
			return strings.TrimSpace(line[i+len("NODE_PATH="):])
		}
	}
	t.Fatalf("no NODE_PATH in output:\n%s", out)
	return ""
}
