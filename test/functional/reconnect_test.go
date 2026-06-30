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
	"sync"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// safeBuffer is an io.Writer the serve subprocess writes to from its own goroutine
// while the test reads it — guarded so the race detector stays quiet.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestServeReconnectsAfterSSHDrop is the killer test for keepalive + auto-reconnect:
// a long-lived `serve` services a ping, then the live SSH connection is forcibly
// dropped on the VM, and we (a) POSITIVELY assert serve logged a reconnect — proof
// the supervisor actually detected the dead link and re-established — and (b) confirm
// a ping delivered over a FRESH connection after the drop is serviced again.
//
// The drop kills the connection's sshd child processes (then restarts the listener so
// the reconnect can succeed); it does not rely on `rc-service restart` alone, which
// can leave established children alive and never drop serve's connection (a false
// green). The post-drop ping uses a freshly-dialed fs, since the test's own original
// connection is dropped too.
func TestServeReconnectsAfterSSHDrop(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-reconnect-" + protocol.NewID()
	cfg := writeReconnectConfig(t, sb, root)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	// Long-lived serve under a private HOME (isolated activity.jsonl / agent.log),
	// capturing stdout so we can assert the reconnect actually happened.
	home := t.TempDir()
	var serveOut safeBuffer
	cmd := exec.Command(iceclimberBin, "serve", "--yes", "--config", cfg, "--transport", "sftp")
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdout = &serveOut
	cmd.Stderr = &serveOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	ctx := context.Background()
	tree := protocol.Tree{Root: root}

	deliverPing := func(fs remotefs.FS) string {
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
	waitPong := func(fs remotefs.FS, name string, within time.Duration) *protocol.Response {
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

	// 1. Baseline: serving works over the initial connection.
	fs1, cleanup1 := dialFS(t, sb, "sftp")
	if r := waitPong(fs1, deliverPing(fs1), 20*time.Second); r.Status != protocol.StatusOK {
		t.Fatalf("baseline pong = %+v, want ok", r)
	}
	cleanup1()

	// 2. Forcibly drop the live SSH connection out from under serve. On OpenSSH 9.8+
	//    each connection is handled by an `sshd-session` child (the listener is a
	//    separate `sshd ... [listener]` process); killing the session children drops
	//    every live connection — serve's, ours, and Lima's forward — while leaving the
	//    listener up so reconnects succeed. Detached + delayed so this control command
	//    returns before its own connection is killed; errors tolerated (the reconnect
	//    assertion below is the real check).
	drop := exec.Command("limactl", "shell", sandboxName, "--", "sudo", "sh", "-c",
		"setsid sh -c 'sleep 1; pkill -KILL -f sshd-session' </dev/null >/dev/null 2>&1 &")
	if err := drop.Run(); err != nil {
		t.Logf("drop command returned %v (tolerated — its own connection may have been killed)", err)
	}

	// 3. Positively assert serve detected the drop and reconnected. Generous deadline:
	//    delayed kill (1s) + keepalive detection (~interval*misses) + backoff + redial.
	if !waitForOutput(t, &serveOut, "reconnected to sandbox", 120*time.Second) {
		t.Fatalf("serve did not log a reconnect after the SSH drop — output:\n%s", serveOut.String())
	}

	// 4. Servicing resumed: a ping over a freshly-dialed connection is serviced.
	fs2, cleanup2 := dialFS(t, sb, "sftp")
	defer cleanup2()
	if r := waitPong(fs2, deliverPing(fs2), 30*time.Second); r.Status != protocol.StatusOK {
		t.Fatalf("post-reconnect pong = %+v, want ok (serve should have auto-reconnected)", r)
	}
}

// waitForOutput polls buf until it contains sub or the deadline passes.
func waitForOutput(t *testing.T, buf *safeBuffer, sub string, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if bytes.Contains([]byte(buf.String()), []byte(sub)) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
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
