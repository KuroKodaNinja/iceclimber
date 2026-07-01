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

const (
	sandboxName      = "iceclimber-sandbox"       // the default musl (Alpine) box
	glibcSandboxName = "iceclimber-sandbox-glibc" // the glibc (Ubuntu) brownfield box
)

// iceclimberBin is the path to the binary built once in TestMain.
var iceclimberBin string

// sandboxConn is everything needed to point iceclimber at the Lima VM. Name is the
// Lima instance (and the sandbox_id used in generated configs), so a test can target
// either the musl or the glibc box.
type sandboxConn struct {
	Name         string
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

// requireSandbox returns connection details for the running musl Lima sandbox, or
// skips the test with an actionable message when it isn't available.
func requireSandbox(t *testing.T) sandboxConn {
	t.Helper()
	sandboxOnce.Do(func() { sandboxInfo, sandboxSkip, sandboxErr = discoverNamed(sandboxName, "make sandbox-up") })
	if sandboxErr != nil {
		t.Fatalf("sandbox setup: %v", sandboxErr)
	}
	if sandboxSkip != "" {
		t.Skip(sandboxSkip)
	}
	return sandboxInfo
}

// glibc box discovery (its own once, so it's independent of the musl box).
var (
	glibcOnce sync.Once
	glibcInfo sandboxConn
	glibcSkip string
	glibcErr  error
)

// requireGlibcSandbox returns connection details for the running glibc Lima sandbox
// (brownfield/manylinux fixture), or skips when it isn't available.
func requireGlibcSandbox(t *testing.T) sandboxConn {
	t.Helper()
	glibcOnce.Do(func() { glibcInfo, glibcSkip, glibcErr = discoverNamed(glibcSandboxName, "make sandbox-glibc-up") })
	if glibcErr != nil {
		t.Fatalf("glibc sandbox setup: %v", glibcErr)
	}
	if glibcSkip != "" {
		t.Skip(glibcSkip)
	}
	return glibcInfo
}

// discoverNamed resolves a Lima instance by name into a sandboxConn, returning a
// skip message (with the given bring-up hint) when limactl/the VM isn't ready.
func discoverNamed(name, upHint string) (sandboxConn, string, error) {
	if _, err := exec.LookPath("limactl"); err != nil {
		return sandboxConn{}, "limactl not found; install Lima and run `" + upHint + "`", nil
	}
	inst, err := limaInstance(name)
	if err != nil {
		return sandboxConn{}, fmt.Sprintf("sandbox %q not found (%v); run `%s`", name, err, upHint), nil
	}
	if inst.Status != "Running" {
		return sandboxConn{}, fmt.Sprintf("sandbox %q is %q, not Running; run `%s`", name, inst.Status, upHint), nil
	}
	if inst.SSHLocalPort == 0 {
		return sandboxConn{}, fmt.Sprintf("sandbox %q has no ssh port yet; is it still booting?", name), nil
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
	return sandboxConn{Name: name, Host: host, Port: inst.SSHLocalPort, User: usr, IdentityFile: identity, KnownHosts: known}, "", nil
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

// sshConfigYAML renders the sandbox_id + ssh block, keyed to sb.Name — the one place
// the ssh fields live, so the various config writers can't drift.
func sshConfigYAML(sb sandboxConn) string {
	return fmt.Sprintf(`sandbox_id: %s
ssh:
  host: %s
  port: %d
  user: %s
  identity_file: %s
  known_hosts: %s
  use_ssh_config: false
`, sb.Name, sb.Host, sb.Port, sb.User, sb.IdentityFile, sb.KnownHosts)
}

// writeYAML writes content to a fresh temp iceclimber.yaml and returns its path.
func writeYAML(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "iceclimber.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeConfig writes a real iceclimber.yaml pointing at the sandbox (no remote_root).
func writeConfig(t *testing.T, sb sandboxConn) string { return writeConfigFor(t, sb, "") }

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

// runIceclimberErr runs the built binary and returns its combined stdout+stderr and the
// exit error WITHOUT failing the test — for asserting an expected non-zero exit (e.g. a
// clean box refusing to serve until bootstrapped). runIceclimber is for the happy path.
func runIceclimberErr(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := exec.Command(iceclimberBin, args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// scheduleRootCleanup removes a test's sandbox root at test end so repeated runs
// don't accumulate ~210MB python installs and fill the VM disk. Best-effort.
// Defaults to the musl box; use scheduleRootCleanupOn for a different instance.
func scheduleRootCleanup(t *testing.T, root string) {
	scheduleRootCleanupOn(t, sandboxName, root)
}

// scheduleRootCleanupOn is scheduleRootCleanup against a named Lima instance.
func scheduleRootCleanupOn(t *testing.T, name, root string) {
	t.Cleanup(func() {
		_ = exec.Command("limactl", "shell", name, "--", "rm", "-rf", root).Run()
	})
}

// writeConfigFor writes an iceclimber.yaml for an arbitrary sandbox (musl or glibc),
// using sb.Name as the sandbox_id and instance. A non-empty root pins remote_root and
// schedules its cleanup; an empty root omits it (e.g. for a probe-only test).
func writeConfigFor(t *testing.T, sb sandboxConn, root string) string {
	t.Helper()
	c := sshConfigYAML(sb)
	if root != "" {
		c += fmt.Sprintf("remote_root: %s\n", root)
		scheduleRootCleanupOn(t, sb.Name, root)
	}
	return writeYAML(t, c)
}
