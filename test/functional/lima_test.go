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
	"testing"
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
	KnownHosts   string // temp file populated by ssh-keyscan
}

type limaJSON struct {
	Status       string `json:"status"`
	Dir          string `json:"dir"`
	SSHLocalPort int    `json:"sshLocalPort"`
}

// requireSandbox returns connection details for the running Lima sandbox, or
// skips the test with an actionable message when it isn't available. This is
// what keeps the functional suite reuse-friendly: boot the VM once, run often.
func requireSandbox(t *testing.T) sandboxConn {
	t.Helper()
	if _, err := exec.LookPath("limactl"); err != nil {
		t.Skip("limactl not found; install Lima and run `make sandbox-up`")
	}
	inst, err := limaInstance(sandboxName)
	if err != nil {
		t.Skipf("sandbox %q not found (%v); run `make sandbox-up`", sandboxName, err)
	}
	if inst.Status != "Running" {
		t.Skipf("sandbox %q is %q, not Running; run `make sandbox-up`", sandboxName, inst.Status)
	}
	if inst.SSHLocalPort == 0 {
		t.Skipf("sandbox %q has no ssh port yet; is it still booting?", sandboxName)
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

	return sandboxConn{
		Host:         host,
		Port:         inst.SSHLocalPort,
		User:         usr,
		IdentityFile: identity,
		KnownHosts:   keyscan(t, host, inst.SSHLocalPort),
	}
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
// (the first matching directive wins, matching ssh's own precedence).
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

// keyscan records the VM's current host key into a temp known_hosts file. Lima
// regenerates host keys per VM, so we scan fresh each run rather than cache.
func keyscan(t *testing.T, host string, port int) string {
	t.Helper()
	var out bytes.Buffer
	cmd := exec.Command("ssh-keyscan", "-p", strconv.Itoa(port), host)
	cmd.Stdout = &out // keys to stdout; progress noise goes to stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ssh-keyscan %s:%d: %v", host, port, err)
	}
	if out.Len() == 0 {
		t.Fatalf("ssh-keyscan returned no host keys for %s:%d", host, port)
	}
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
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
