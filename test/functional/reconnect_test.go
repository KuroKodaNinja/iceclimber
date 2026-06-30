//go:build functional

package functional

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestServeReconnectsAfterSSHDrop is the killer test for keepalive + auto-reconnect:
// a long-lived `serve` services a ping, then sshd is restarted on the VM (dropping
// the live SSH/SFTP connection), and a ping delivered AFTER the drop is still
// serviced — proving the supervisor detected the dead link (keepalive closes the
// client fast), reconnected, and resumed servicing with no manual restart.
//
// The second ping is delivered while the link is down (the durable maildir holds it),
// so this also covers "a request issued during the outage is serviced once the link
// returns."
func TestServeReconnectsAfterSSHDrop(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-reconnect-" + protocol.NewID()
	cfg := writeReconnectConfig(t, sb, root)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	// Long-lived serve under a private HOME (isolated activity.jsonl / agent.log).
	home := t.TempDir()
	cmd := exec.Command(iceclimberBin, "serve", "--yes", "--config", cfg, "--transport", "sftp")
	cmd.Env = append(os.Environ(), "HOME="+home)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	fs, cleanup := dialFS(t, sb, "sftp")
	defer cleanup()
	ctx := context.Background()
	tree := protocol.Tree{Root: root}

	deliverPing := func() string {
		id := protocol.NewID()
		name := protocol.RequestName(id)
		data, _ := json.Marshal(protocol.Request{
			SchemaVersion: protocol.SchemaVersion, ID: id, Type: "ping",
			CreatedAt: time.Now().UTC(), Params: json.RawMessage("{}"),
		})
		if err := protocol.Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
			t.Fatalf("deliver ping: %v", err)
		}
		return name
	}
	// waitPong polls for a serviced response until the deadline.
	waitPong := func(name string, within time.Duration) *protocol.Response {
		deadline := time.Now().Add(within)
		for time.Now().Before(deadline) {
			if r, err := protocol.ReadResponse(ctx, fs, tree, name); err == nil && r != nil {
				return r
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("no pong for %s within %s", name, within)
		return nil
	}

	// 1. Baseline: serving works.
	if r := waitPong(deliverPing(), 20*time.Second); r.Status != protocol.StatusOK {
		t.Fatalf("baseline pong = %+v, want ok", r)
	}

	// 2. Drop the connection out from under serve by restarting sshd on the VM.
	if err := exec.Command("limactl", "shell", sandboxName, "--", "sudo", "sh", "-c", "rc-service sshd restart").Run(); err != nil {
		t.Skipf("cannot restart sshd on the sandbox: %v", err)
	}

	// 3. A ping delivered after the drop must still be serviced — the supervisor
	//    reconnects (keepalive-detected dead link + capped backoff) and resumes.
	//    Generous deadline: keepalive detection (~interval*misses) + backoff + service.
	if r := waitPong(deliverPing(), 90*time.Second); r.Status != protocol.StatusOK {
		t.Fatalf("post-drop pong = %+v, want ok (serve should auto-reconnect)", r)
	}
}

// writeReconnectConfig mirrors writeConfigRoot but pins a short keepalive_interval so
// a dropped link is detected in seconds (not the OS TCP timeout), keeping the test
// fast.
func writeReconnectConfig(t *testing.T, sb sandboxConn, root string) string {
	t.Helper()
	content := fmt.Sprintf(`sandbox_id: %s
ssh:
  host: %s
  port: %d
  user: %s
  identity_file: %s
  known_hosts: %s
  use_ssh_config: false
  keepalive_interval: 2
remote_root: %s
`, sandboxName, sb.Host, sb.Port, sb.User, sb.IdentityFile, sb.KnownHosts, root)
	path := filepath.Join(t.TempDir(), "iceclimber.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	scheduleRootCleanup(t, root)
	return path
}
