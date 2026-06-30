//go:build functional

package functional

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const sandboxName = "iceclimber-sandbox"

// iceclimberBin is the path to the binary built once in TestMain.
var iceclimberBin string

// sandboxConn is everything needed to point iceclimber at the Lima VM.
type sandboxConn struct {
	Host         string
	Port         int
	User         string
	IdentityFile string
	KnownHosts   string // temp file populated once by ssh-keyscan
}

type limaJSON struct {
	Status       string `json:"status"`
	Dir          string `json:"dir"`
	SSHLocalPort int    `json:"sshLocalPort"`
}

// The sandbox is discovered (and its host key scanned) exactly once per run: the
// VM's key is stable for its lifetime, and re-scanning on every test caused
// ssh-keyscan to fail intermittently under the suite's connection churn.
var (
	sandboxOnce sync.Once
	sandboxInfo sandboxConn
	sandboxSkip string
	sandboxErr  error
)

// requireSandbox returns connection details for the running Lima sandbox, or
// skips the test with an actionable message when it isn't available.
func requireSandbox(t *testing.T) sandboxConn {
	t.Helper()
	sandboxOnce.Do(func() { sandboxInfo, sandboxSkip, sandboxErr = discoverSandbox() })
	if sandboxErr != nil {
		t.Fatalf("sandbox setup: %v", sandboxErr)
	}
	if sandboxSkip != "" {
		t.Skip(sandboxSkip)
	}
	return sandboxInfo
}

func discoverSandbox() (sandboxConn, string, error) {
	if _, err := exec.LookPath("limactl"); err != nil {
		return sandboxConn{}, "limactl not found; install Lima and run `make sandbox-up`", nil
	}
	inst, err := limaInstance(sandboxName)
	if err != nil {
		return sandboxConn{}, fmt.Sprintf("sandbox %q not found (%v); run `make sandbox-up`", sandboxName, err), nil
	}
	if inst.Status != "Running" {
		return sandboxConn{}, fmt.Sprintf("sandbox %q is %q, not Running; run `make sandbox-up`", sandboxName, inst.Status), nil
	}
	if inst.SSHLocalPort == 0 {
		return sandboxConn{}, fmt.Sprintf("sandbox %q has no ssh port yet; is it still booting?", sandboxName), nil
	}

	host := sshConfigField(inst.Dir, "Hostname")
	if host == "" {
		host = "127.0.0.1"
	}
	usr := sshConfigField(inst.Dir, "User")
	if usr == "" {
		if u, err := user.Current(); err == nil {
			usr = u.Username
		}
	}
	identity := sshConfigField(inst.Dir, "IdentityFile")
	if identity == "" {
		home, _ := os.UserHomeDir()
		identity = filepath.Join(home, ".lima", "_config", "user")
	}

	known, err := keyscanToFile(host, inst.SSHLocalPort)
	if err != nil {
		return sandboxConn{}, "", err
	}
	return sandboxConn{Host: host, Port: inst.SSHLocalPort, User: usr, IdentityFile: identity, KnownHosts: known}, "", nil
}

func limaInstance(name string) (limaJSON, error) {
	var out bytes.Buffer
	cmd := exec.Command("limactl", "list", "--json", name)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return limaJSON{}, fmt.Errorf("limactl list: %w", err)
	}
	var inst limaJSON
	if err := json.NewDecoder(&out).Decode(&inst); err != nil {
		return limaJSON{}, fmt.Errorf("instance not present")
	}
	return inst, nil
}

// sshConfigField reads a single value from the instance's generated ssh.config
// (first matching directive wins, matching ssh's own precedence).
func sshConfigField(dir, key string) string {
	data, err := os.ReadFile(filepath.Join(dir, "ssh.config"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.EqualFold(fields[0], key) {
			return strings.Trim(fields[1], `"`)
		}
	}
	return ""
}

// keyscanToFile records the VM's host key into a temp known_hosts file, with a
// few retries (ssh-keyscan can transiently fail under load). Called once per run.
func keyscanToFile(host string, port int) (string, error) {
	f, err := os.CreateTemp("", "iceclimber-known_hosts-*")
	if err != nil {
		return "", err
	}
	defer f.Close()

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		var out bytes.Buffer
		cmd := exec.Command("ssh-keyscan", "-T", "10", "-p", strconv.Itoa(port), host)
		cmd.Stdout = &out // keys to stdout; progress noise goes to stderr
		err := cmd.Run()
		if err == nil && out.Len() > 0 {
			if _, werr := f.Write(out.Bytes()); werr != nil {
				return "", werr
			}
			return f.Name(), nil
		}
		lastErr = fmt.Errorf("ssh-keyscan %s:%d: %v", host, port, err)
		time.Sleep(500 * time.Millisecond)
	}
	return "", lastErr
}

// writeConfig writes a real iceclimber.yaml pointing at the sandbox.
func writeConfig(t *testing.T, sb sandboxConn) string {
	t.Helper()
	content := fmt.Sprintf(`sandbox_id: %s
ssh:
  host: %s
  port: %d
  user: %s
  identity_file: %s
  known_hosts: %s
  use_ssh_config: false
`, sandboxName, sb.Host, sb.Port, sb.User, sb.IdentityFile, sb.KnownHosts)
	path := filepath.Join(t.TempDir(), "iceclimber.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runIceclimber runs the built binary and fails the test on a non-zero exit.
func runIceclimber(t *testing.T, args ...string) []byte {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(iceclimberBin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("iceclimber %v: %v\nstderr: %s", args, err, stderr.String())
	}
	return stdout.Bytes()
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// scheduleRootCleanup removes a test's sandbox root at test end so repeated runs
// don't accumulate ~210MB python installs and fill the VM disk. Best-effort.
func scheduleRootCleanup(t *testing.T, root string) {
	t.Cleanup(func() {
		_ = exec.Command("limactl", "shell", sandboxName, "--", "rm", "-rf", root).Run()
	})
}
