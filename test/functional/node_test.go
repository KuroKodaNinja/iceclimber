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

// TestNodeInstall installs a Node runtime into the real Alpine/musl VM via the
// node.install verb and proves it executes by running bin/node by absolute path.
// Node ≥24 is required for the arm64-musl build (older musl builds are x64-only).
func TestNodeInstall(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-node-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	fs, cleanup := dialFS(t, sb, "sftp")
	defer cleanup()
	ctx := context.Background()
	tree := protocol.Tree{Root: root}

	id := protocol.NewID()
	name := protocol.RequestName(id)
	data, _ := json.Marshal(protocol.Request{
		SchemaVersion: protocol.SchemaVersion, ID: id, Type: "node.install",
		CreatedAt: time.Now().UTC(), Params: json.RawMessage(`{"version":"24"}`),
	})
	if err := protocol.Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
		t.Fatalf("deliver node.install: %v", err)
	}

	runIceclimber(t, "serve", "--once", "--config", cfg, "--transport", "sftp")

	resp, err := protocol.ReadResponse(ctx, fs, tree, name)
	if err != nil {
		t.Fatalf("read node.install response: %v", err)
	}
	if resp.Status != protocol.StatusOK {
		t.Fatalf("node.install status = %q, error = %+v", resp.Status, resp.Error)
	}
	var r struct {
		Version string `json:"version"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !strings.HasPrefix(r.Version, "24.") || !strings.HasSuffix(r.Path, "/bin/node") {
		t.Fatalf("result = %+v, want version 24.x and a bin/node path", r)
	}

	// Prove it runs by its absolute path (the §2 contract).
	out := limaSh(t, remoteQuote(r.Path)+" --version")
	if !strings.HasPrefix(strings.TrimSpace(out), "v24.") {
		t.Errorf("node --version = %q, want v24.x", strings.TrimSpace(out))
	}
}

// remoteQuote single-quotes a path for a remote sh -c.
func remoteQuote(p string) string { return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'" }
