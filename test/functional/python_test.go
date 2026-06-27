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
// The ExecFS push path is covered by the fast unit test (internal/python
// TestPushTarGz over a local shell); a full interpreter over ExecFS is thousands
// of round trips, so it's intentionally not exercised in the suite here.
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
