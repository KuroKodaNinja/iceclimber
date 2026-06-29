//go:build functional

package functional

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestPythonInstall installs a real musl CPython onto the Alpine VM over SFTP via
// the CLI (with idempotency), then services a python.install request through the
// maildir — proving both paths. The interpreter is run by install's own verify
// step, so a successful install is proof it executes on musl.
//
// The SFTP-less (ExecFS) path now pushes the whole tree in one `tar` round trip
// (the bulk transfer) and is exercised end to end by TestPythonInstallOverExec.
func TestPythonInstall(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-py-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)

	// Tree must exist for the protocol-path step below.
	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	// CLI path: a real install.
	out := runIceclimber(t, "install", "python", "3.12", "--config", cfg, "--transport", "sftp")
	if !strings.Contains(string(out), "python 3.12.") || !strings.Contains(string(out), "/runtimes/python/") {
		t.Errorf("install output unexpected:\n%s", out)
	}

	// Idempotent re-install.
	out2 := runIceclimber(t, "install", "python", "3.12", "--config", cfg, "--transport", "sftp")
	if !strings.Contains(string(out2), "already installed") {
		t.Errorf("expected idempotent re-install, got:\n%s", out2)
	}

	// Protocol path: deliver python.install, run one serve cycle, read the result.
	fs, cleanup := dialFS(t, sb, "sftp")
	defer cleanup()
	ctx := context.Background()
	tree := protocol.Tree{Root: root}

	id := protocol.NewID()
	name := protocol.RequestName(id)
	req := protocol.Request{
		SchemaVersion: protocol.SchemaVersion,
		ID:            id,
		Type:          "python.install",
		CreatedAt:     time.Now().UTC(),
		Params:        json.RawMessage(`{"version":"3.12"}`),
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
		t.Fatalf("deliver python.install: %v", err)
	}
	runIceclimber(t, "serve", "--once", "--config", cfg, "--transport", "sftp")

	resp, err := protocol.ReadResponse(ctx, fs, tree, name)
	if err != nil {
		t.Fatalf("read python.install response: %v", err)
	}
	if resp.Status != protocol.StatusOK {
		t.Fatalf("python.install status = %q, error = %+v", resp.Status, resp.Error)
	}
	var result struct {
		Version          string `json:"version"`
		Path             string `json:"path"`
		AlreadyInstalled bool   `json:"already_installed"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !strings.HasPrefix(result.Version, "3.12.") || result.Path == "" {
		t.Errorf("python.install result = %+v", result)
	}
}

// TestPythonInstallOverExec installs a real CPython onto the sandbox over the
// ExecFS transport (no SFTP), proving the bulk `tar` transfer works on the
// sandbox's BusyBox: the whole runtime tree lands in one exec, executable bits
// and symlinks intact, and install's own verify step runs the interpreter.
func TestPythonInstallOverExec(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-pyexec-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "exec")

	out := runIceclimber(t, "install", "python", "3.12", "--config", cfg, "--transport", "exec")
	if !strings.Contains(string(out), "python 3.12.") || !strings.Contains(string(out), "/runtimes/python/") {
		t.Errorf("install over exec output unexpected:\n%s", out)
	}

	// Idempotent re-install over exec, same as the SFTP path.
	if out2 := runIceclimber(t, "install", "python", "3.12", "--config", cfg, "--transport", "exec"); !strings.Contains(string(out2), "already installed") {
		t.Errorf("expected idempotent re-install over exec, got:\n%s", out2)
	}

	// The tree must actually be on the VM, and its bin/python3 must run on musl.
	py := strings.TrimSpace(limaSh(t, "ls "+root+"/runtimes/python/*/bin/python3 2>/dev/null | head -1"))
	if py == "" {
		t.Fatal("no python under runtimes/python after exec-transport install")
	}
	if v := limaSh(t, remoteQuote(py)+" --version 2>&1"); !strings.Contains(v, "3.12") {
		t.Errorf("python --version = %q, want 3.12.x", strings.TrimSpace(v))
	}
	// The bulk tar push must preserve symlinks on BusyBox: bin/python3 is a
	// symlink to python3.x in the PBS layout, so `tar` (not per-file ln -s) must
	// have recreated it. `test -L` is the portable check.
	if got := strings.TrimSpace(limaSh(t, "test -L "+remoteQuote(py)+" && echo symlink || echo regular")); got != "symlink" {
		t.Errorf("bin/python3 is %q on the VM, want a symlink preserved by the bulk tar push", got)
	}
}
