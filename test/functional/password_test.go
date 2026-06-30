//go:build functional

package functional

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestPasswordAuth_Automated proves password auth works end to end, headless-
// friendly: it runs `iceclimber probe` under a PTY with NO identity file and NO
// ssh-agent, so the only available method is password — iceclimber prompts no-echo
// on the controlling terminal (the PTY slave) and we feed the password to the
// master. A successful probe means the whole password path connected.
//
// It mutates the shared sandbox additively (sets a password + ensures
// PasswordAuthentication yes; key auth still works for other tests). Skips with a
// clear reason if the VM can't be configured for password auth.
func TestPasswordAuth_Automated(t *testing.T) {
	sb := requireSandbox(t)
	const password = "iceclimber-test-pw"
	enablePasswordAuth(t, sb, password)

	cfg := writePasswordConfig(t, sb, true)
	cmd := exec.Command(iceclimberBin, "probe", "--config", cfg)
	// Force the password path: no agent (so no public-key method is offered).
	cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("start under pty: %v", err)
	}
	defer func() { _ = ptmx.Close() }()

	// Stream the child's output; when the no-echo password prompt appears, type the
	// password. Collect everything for the final assertion.
	var seen bytes.Buffer
	typed := false
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1024)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				seen.Write(buf[:n])
				if !typed && strings.Contains(strings.ToLower(seen.String()), "password:") {
					typed = true
					_, _ = io.WriteString(ptmx, password+"\n")
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(45 * time.Second):
		t.Fatalf("probe under pty timed out; output so far:\n%s", seen.String())
	}
	_ = cmd.Wait()

	out := seen.String()
	if !typed {
		t.Fatalf("never saw a password prompt; output:\n%s", out)
	}
	if !strings.Contains(out, "os/arch:") {
		t.Fatalf("password auth did not complete a probe (no fingerprint):\n%s", out)
	}
}

// TestPasswordAuth_NoMethodFailsClearly: with no identity, no agent, and password
// auth NOT opted in, iceclimber fails fast with an actionable message — before any
// prompt or dial. Deterministic (no tty needed), the guard that proves interactive
// auth is opt-in.
func TestPasswordAuth_NoMethodFailsClearly(t *testing.T) {
	sb := requireSandbox(t)
	cfg := writePasswordConfig(t, sb, false) // password_auth: false, no identity_file
	cmd := exec.Command(iceclimberBin, "probe", "--config", cfg)
	cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure with no auth method available; output:\n%s", out)
	}
	if !strings.Contains(string(out), "no SSH auth method available") {
		t.Errorf("want the actionable 'no SSH auth method available' message; got:\n%s", out)
	}
}

// writePasswordConfig writes an iceclimber.yaml with NO identity_file (so key auth
// is unavailable), password auth toggled per the arg, and ssh_config disabled.
func writePasswordConfig(t *testing.T, sb sandboxConn, passwordAuth bool) string {
	t.Helper()
	content := fmt.Sprintf(`sandbox_id: %s
ssh:
  host: %s
  port: %d
  user: %s
  known_hosts: %s
  use_ssh_config: false
  password_auth: %t
`, sandboxName, sb.Host, sb.Port, sb.User, sb.KnownHosts, passwordAuth)
	path := filepath.Join(t.TempDir(), "iceclimber.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// enablePasswordAuth sets the sandbox user's password and ensures sshd accepts
// password auth, reloading config without dropping existing connections (SIGHUP /
// OpenRC reload). Additive: key auth keeps working. Skips on setup failure.
func enablePasswordAuth(t *testing.T, sb sandboxConn, password string) {
	t.Helper()
	sh := func(cmd string) error {
		return exec.Command("limactl", "shell", sandboxName, "--", "sudo", "sh", "-c", cmd).Run()
	}
	if err := sh(fmt.Sprintf("echo '%s:%s' | chpasswd", sb.User, password)); err != nil {
		t.Skipf("cannot set sandbox password (chpasswd): %v", err)
	}
	// sshd_config Includes sshd_config.d/*.conf BEFORE the main file and takes the
	// FIRST value, and cloud-init drops a 50-*.conf with PasswordAuthentication no.
	// A 00-*.conf sorts earlier, so its "yes" wins.
	if err := sh("printf 'PasswordAuthentication yes\\nKbdInteractiveAuthentication yes\\n' > /etc/ssh/sshd_config.d/00-iceclimber-test.conf"); err != nil {
		t.Skipf("cannot write sshd drop-in: %v", err)
	}
	// Restart so the new config takes effect (drops our control session mid-command;
	// tolerated — the connecting probe below is the real check). Verify it took.
	_ = sh("rc-service sshd restart")
	time.Sleep(2 * time.Second)
	eff, _ := exec.Command("limactl", "shell", sandboxName, "--", "sudo", "sshd", "-T").CombinedOutput()
	if !strings.Contains(strings.ToLower(string(eff)), "passwordauthentication yes") {
		t.Skipf("could not enable password auth on the sandbox (effective config still 'no')")
	}
}
