//go:build functional

package functional

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// TestWebFetch exercises the sandbox-venue web.fetch on Alpine (busybox wget):
// a small GET returns inline + writes an audit line; a large body lands as a
// content-addressed blob in the sandbox.
func TestWebFetch(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-web-" + protocol.NewID()
	auditFile := filepath.Join(t.TempDir(), "audit.jsonl")
	cfg := writeConfigWeb(t, sb, root, auditFile)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	// Small GET → inline, plus an audit line.
	out := runIceclimber(t, "web", "fetch", "https://example.com", "--config", cfg, "--transport", "sftp")
	if !strings.Contains(string(out), "200") || !strings.Contains(string(out), "Example Domain") {
		t.Errorf("web fetch output:\n%s", out)
	}
	audit, err := os.ReadFile(auditFile)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if !strings.Contains(string(audit), `"status_code":200`) || !strings.Contains(string(audit), `"venue":"sandbox-exec"`) {
		t.Errorf("audit line missing fields:\n%s", audit)
	}

	// Large body (>16 KB) → blob; confirm it exists in the sandbox.
	fs, cleanup := dialFS(t, sb, "sftp")
	defer cleanup()
	ctx := context.Background()
	tree := protocol.Tree{Root: root}

	resp := webFetchViaServe(t, ctx, fs, tree, cfg, `{"url":"https://www.rfc-editor.org/rfc/rfc1918.txt"}`)
	if resp.Status != protocol.StatusOK {
		t.Fatalf("web.fetch status = %q, error = %+v", resp.Status, resp.Error)
	}
	var r struct {
		StatusCode int    `json:"status_code"`
		Venue      string `json:"venue"`
		BodyBlob   string `json:"body_blob"`
		BodyInline string `json:"body_inline"`
	}
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 200 || r.Venue != "sandbox-exec" {
		t.Errorf("result = %+v", r)
	}
	if r.BodyBlob == "" {
		t.Fatalf("expected body_blob for a large body, got inline len %d", len(r.BodyInline))
	}
	if _, err := fs.ReadFile(ctx, path.Join(root, r.BodyBlob)); err != nil {
		t.Errorf("blob %s not found in sandbox: %v", r.BodyBlob, err)
	}
}

func webFetchViaServe(t *testing.T, ctx context.Context, fs remotefs.FS, tree protocol.Tree, cfg, params string) *protocol.Response {
	t.Helper()
	id := protocol.NewID()
	name := protocol.RequestName(id)
	req := protocol.Request{
		SchemaVersion: protocol.SchemaVersion,
		ID:            id,
		Type:          "web.fetch",
		CreatedAt:     time.Now().UTC(),
		Params:        json.RawMessage(params),
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
		t.Fatalf("deliver web.fetch: %v", err)
	}
	runIceclimber(t, "serve", "--once", "--config", cfg, "--transport", "sftp")
	resp, err := protocol.ReadResponse(ctx, fs, tree, name)
	if err != nil {
		t.Fatalf("read web.fetch response: %v", err)
	}
	return resp
}

func writeConfigWeb(t *testing.T, sb sandboxConn, root, auditLog string) string {
	t.Helper()
	content := fmt.Sprintf(`sandbox_id: %s
ssh:
  host: %s
  port: %d
  user: %s
  identity_file: %s
  known_hosts: %s
remote_root: %s
audit_log: %s
`, sandboxName, sb.Host, sb.Port, sb.User, sb.IdentityFile, sb.KnownHosts, root, auditLog)
	p := filepath.Join(t.TempDir(), "iceclimber.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	scheduleRootCleanup(t, root)
	return p
}
