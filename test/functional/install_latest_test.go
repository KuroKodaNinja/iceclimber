//go:build functional

package functional

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/python"
)

// TestInstallLatestResolves proves the agent-facing "install the current runtime without
// naming a version" affordance end to end: a python.install request carrying version "latest"
// (what `popo python.install` sends when the version is omitted) is resolved server-side to the
// recommended line and installed — so Nana never has to guess a number and can't pick an older
// one. Python is the cheapest runtime to install on the musl box.
func TestInstallLatestResolves(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-latest-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

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
		Params:        json.RawMessage(`{"version":"latest"}`), // no specific version → recommended
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
		t.Fatalf("deliver python.install latest: %v", err)
	}
	runIceclimber(t, "serve", "--once", "--config", cfg, "--transport", "sftp")

	resp, err := protocol.ReadResponse(ctx, fs, tree, name)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.Status != protocol.StatusOK {
		t.Fatalf("python.install(latest) status = %q, error = %+v", resp.Status, resp.Error)
	}
	var result struct {
		Version string `json:"version"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	// "latest" must resolve to the recommended line (single source of truth), not some other
	// version — proving the resolution reached the installer.
	if !strings.HasPrefix(result.Version, python.RecommendedLine+".") || result.Path == "" {
		t.Errorf("python.install(latest) = %+v, want the recommended line %s.x installed", result, python.RecommendedLine)
	}
}
