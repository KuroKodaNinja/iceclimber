//go:build functional

package functional

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// TestPipInstall installs packages into a real runtime on the VM, pointing pip at
// real PyPI (stand-in mirror; the egress restriction is not modeled). Covers the
// CLI path, the pip.install verb (unversioned → native co-resolution with a
// recorded sha256), and a resolution failure.
func TestPipInstall(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-pip-" + protocol.NewID()
	cfg := writeConfigPip(t, sb, root, "https://pypi.org/simple")

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")
	runIceclimber(t, "install", "python", "3.12", "--config", cfg, "--transport", "sftp")

	// CLI path: a pinned pure-python package.
	out := runIceclimber(t, "install", "pip", "six==1.16.0", "--config", cfg, "--transport", "sftp", "--python", "3.12")
	if !strings.Contains(string(out), "installed six 1.16.0") || !strings.Contains(string(out), "0 failed") {
		t.Errorf("install pip output:\n%s", out)
	}
	// Confirm it imports on musl.
	imp := fmt.Sprintf(`for d in %s/runtimes/python/*/bin/python3; do "$d" -c 'import six; print(six.__version__)'; done`, root)
	if got := limaSh(t, imp); !strings.Contains(got, "1.16.0") {
		t.Errorf("import six in sandbox = %q", got)
	}

	fs, cleanup := dialFS(t, sb, "sftp")
	defer cleanup()
	ctx := context.Background()
	tree := protocol.Tree{Root: root}

	// Protocol path, unversioned: native resolution with a recorded version + sha256.
	resp := pipInstallViaServe(t, ctx, fs, tree, cfg,
		`{"python_version":"3.12","packages":[{"name":"certifi"}]}`)
	if resp.Status != protocol.StatusOK {
		t.Fatalf("pip.install status = %q, error = %+v", resp.Status, resp.Error)
	}
	var oc pkg.Outcome
	if err := json.Unmarshal(resp.Result, &oc); err != nil {
		t.Fatalf("unmarshal outcome: %v", err)
	}
	if len(oc.Installed) == 0 {
		t.Fatalf("no packages installed: %+v", oc)
	}
	if got := oc.Installed[0]; got.Name != "certifi" || got.Version == "" || got.SHA256 == "" || got.Tier != "mirror" {
		t.Errorf("installed entry = %+v (want certifi with resolved version + sha256 + tier mirror)", got)
	}

	// Failure: unknown package → resolution_failed.
	fail := pipInstallViaServe(t, ctx, fs, tree, cfg,
		`{"python_version":"3.12","packages":[{"name":"this-pkg-does-not-exist-xyz","version":"9.9.9"}]}`)
	if fail.Status != protocol.StatusError || fail.Error == nil || fail.Error.Code != "resolution_failed" {
		t.Errorf("unknown-package response = %+v, want status error code resolution_failed", fail)
	}
}

// pipInstallViaServe delivers a pip.install request, runs one serve cycle, and
// returns the response.
func pipInstallViaServe(t *testing.T, ctx context.Context, fs remotefs.FS, tree protocol.Tree, cfg, params string) *protocol.Response {
	t.Helper()
	id := protocol.NewID()
	name := protocol.RequestName(id)
	req := protocol.Request{
		SchemaVersion: protocol.SchemaVersion,
		ID:            id,
		Type:          "pip.install",
		CreatedAt:     time.Now().UTC(),
		Params:        json.RawMessage(params),
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
		t.Fatalf("deliver pip.install: %v", err)
	}
	runIceclimber(t, "serve", "--once", "--config", cfg, "--transport", "sftp")
	resp, err := protocol.ReadResponse(ctx, fs, tree, name)
	if err != nil {
		t.Fatalf("read pip.install response: %v", err)
	}
	return resp
}

func writeConfigPip(t *testing.T, sb sandboxConn, root, indexURL string) string {
	t.Helper()
	content := fmt.Sprintf(`sandbox_id: %s
ssh:
  host: %s
  port: %d
  user: %s
  identity_file: %s
  known_hosts: %s
remote_root: %s
pip:
  index_url: %s
`, sandboxName, sb.Host, sb.Port, sb.User, sb.IdentityFile, sb.KnownHosts, root, indexURL)
	path := filepath.Join(t.TempDir(), "iceclimber.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	scheduleRootCleanup(t, root)
	return path
}

// limaSh runs a shell snippet inside the sandbox and returns its combined output.
func limaSh(t *testing.T, script string) string {
	t.Helper()
	out, err := exec.Command("limactl", "shell", sandboxName, "--", "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("limactl shell: %v\n%s", err, out)
	}
	return string(out)
}
