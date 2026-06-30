//go:build functional

package functional

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestSupervisedServe drives `serve --supervise --once` against the VM with piped
// approval input, exercising the inline-approval path end to end: the dispatcher
// gate denies an install, and the egress approver both approves (the fetch then
// returns its real result in one pass) and denies a controller-venue fetch. An
// isolated approvals_file keeps the gate state clean.
func TestSupervisedServe(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-sup-" + protocol.NewID()
	approvals := filepath.Join(t.TempDir(), "approvals.json")
	cfg := writeSupervisedConfig(t, sb, root, approvals)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	fs, cleanup := dialFS(t, sb, "sftp")
	defer cleanup()
	ctx := context.Background()
	tree := protocol.Tree{Root: root}

	deliver := func(typ, params string) (string, string) {
		id := protocol.NewID()
		name := protocol.RequestName(id)
		data, _ := json.Marshal(protocol.Request{
			SchemaVersion: protocol.SchemaVersion, ID: id, Type: typ,
			CreatedAt: time.Now().UTC(), Params: json.RawMessage(params),
		})
		if err := protocol.Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
			t.Fatalf("deliver %s: %v", typ, err)
		}
		return id, name
	}
	// serveSupervised runs one supervised cycle, feeding stdin as the approval input.
	serveSupervised := func(stdin string) {
		var stderr bytes.Buffer
		cmd := exec.Command(iceclimberBin, "serve", "--once", "--supervise", "--config", cfg, "--transport", "sftp")
		cmd.Stdin = strings.NewReader(stdin)
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("serve --supervise: %v\n%s", err, stderr.String())
		}
	}
	read := func(name string) *protocol.Response {
		r, err := protocol.ReadResponse(ctx, fs, tree, name)
		if err != nil {
			t.Fatalf("read response %s: %v", name, err)
		}
		return r
	}

	// 1. The gate denies a pip.install — the handler never runs (no Python needed).
	_, n1 := deliver("pip.install", `{"python_version":"3.12","packages":[{"name":"rich"}]}`)
	serveSupervised("n\n")
	if r := read(n1); r.Status != protocol.StatusError || r.Error == nil || r.Error.Code != "operator_denied" {
		t.Errorf("gate deny: got %+v, want error operator_denied", r)
	}

	// 2. Inline-approve an unlisted controller-venue fetch — it proceeds in the same
	//    pass and returns the real result.
	_, n2 := deliver("web.fetch", `{"url":"https://example.com"}`)
	serveSupervised("y\n")
	if r := read(n2); r.Status != protocol.StatusOK {
		t.Errorf("egress approve: got %+v, want ok", r)
	}

	// 3. Inline-deny a controller-venue fetch — egress_denied, no fetch.
	_, n3 := deliver("web.fetch", `{"url":"https://example.org"}`)
	serveSupervised("n\n")
	if r := read(n3); r.Status != protocol.StatusError || r.Error == nil || r.Error.Code != "egress_denied" {
		t.Errorf("egress deny: got %+v, want error egress_denied", r)
	}
}

func writeSupervisedConfig(t *testing.T, sb sandboxConn, root, approvals string) string {
	t.Helper()
	content := fmt.Sprintf(`sandbox_id: %s
ssh:
  host: %s
  port: %d
  user: %s
  identity_file: %s
  known_hosts: %s
  use_ssh_config: false
remote_root: %s
approvals_file: %s
`, sandboxName, sb.Host, sb.Port, sb.User, sb.IdentityFile, sb.KnownHosts, root, approvals)
	path := filepath.Join(t.TempDir(), "iceclimber.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	scheduleRootCleanup(t, root)
	return path
}
