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

// TestPingPong drives a full ping/pong round-trip through the maildir on the real
// VM, over both transports: `bootstrap` (which runs its own smoke test), then an
// explicit deliver -> `serve --once` -> read-pong cycle.
func TestPingPong(t *testing.T) {
	sb := requireSandbox(t)
	for _, transport := range []string{"exec", "sftp"} {
		t.Run(transport, func(t *testing.T) {
			root := "/tmp/iceclimber-e2e-" + protocol.NewID()
			cfg := writeConfigRoot(t, sb, root)

			// bootstrap creates the tree and runs the §7 ping/pong smoke test.
			out := runIceclimber(t, "bootstrap", "--config", cfg, "--transport", transport)
			if !strings.Contains(string(out), "smoke test") {
				t.Errorf("bootstrap output lacks smoke-test confirmation:\n%s", out)
			}

			// Explicitly exercise serve --once: deliver a ping, run one cycle, read the pong.
			fs, cleanup := dialFS(t, sb, transport)
			defer cleanup()
			ctx := context.Background()
			tree := protocol.Tree{Root: root}

			id := protocol.NewID()
			name := protocol.RequestName(id)
			req := protocol.Request{
				SchemaVersion: protocol.SchemaVersion,
				ID:            id,
				Type:          "ping",
				CreatedAt:     time.Now().UTC(),
				Params:        json.RawMessage("{}"),
			}
			data, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			if err := protocol.Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
				t.Fatalf("deliver ping: %v", err)
			}

			runIceclimber(t, "serve", "--once", "--config", cfg, "--transport", transport)

			resp, err := protocol.ReadResponse(ctx, fs, tree, name)
			if err != nil {
				t.Fatalf("read pong: %v", err)
			}
			if resp.Status != protocol.StatusOK || resp.ID != id {
				t.Errorf("pong = %+v, want status ok with id %s", resp, id)
			}
			var pong struct {
				PopoVersion string `json:"popo_version"`
			}
			if err := json.Unmarshal(resp.Result, &pong); err != nil {
				t.Fatalf("unmarshal pong result: %v", err)
			}
			if pong.PopoVersion == "" {
				t.Error("pong missing popo_version")
			}
		})
	}
}

// writeConfigRoot writes a config pinned to a specific remote_root, so each E2E
// run gets an isolated tree and bootstrap/serve skip root probing.
func writeConfigRoot(t *testing.T, sb sandboxConn, root string) string {
	return writeConfigFor(t, sb, root)
}
