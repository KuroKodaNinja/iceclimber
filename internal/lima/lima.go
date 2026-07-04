// Package lima is a thin wrapper over the `limactl` CLI (§18) — NOT a VM manager. It
// provisions and inspects Lima instances just enough for `iceclimber sandbox create` to
// bring up a local box, read its SSH details, record its host key, apply the egress
// firewall, and tear it down. Every operation shells out to `limactl` (or `ssh-keyscan`);
// nothing here reimplements Lima. The SSH-detail extraction mirrors the functional
// harness (test/functional/lima_test.go) so the product and the tests can't drift.
package lima

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// UbuntuTemplate is the embedded minimal Ubuntu (vz) Lima template used by `sandbox
// create` (a non-sudo `agent` user, SSH on, no toolchain). AirgapScript is the egress
// lockdown applied at bootstrap. Exposed as bytes so the CLI layer can hand them to
// limactl / the sandbox without re-reading files.
var (
	//go:embed ubuntu.yaml
	UbuntuTemplate []byte

	//go:embed airgap.sh
	AirgapScript []byte
)

// Available reports whether the `limactl` CLI is on PATH. Callers gate every VM
// operation on this so a machine without Lima gets a clear message, not an exec error.
func Available() bool {
	_, err := exec.LookPath("limactl")
	return err == nil
}

// Info is the subset of `limactl list --json` iceclimber needs: run state, the instance
// directory (which holds ssh.config), and the forwarded local SSH port.
type Info struct {
	Status       string
	Dir          string
	SSHLocalPort int
}

// limaJSON is the wire shape of one `limactl list --json` record.
type limaJSON struct {
	Status       string `json:"status"`
	Dir          string `json:"dir"`
	SSHLocalPort int    `json:"sshLocalPort"`
}

// Instance resolves a Lima instance by name. exists=false (with a nil error) means the
// instance is simply not present; a non-nil error is a real limactl failure. Mirrors the
// functional harness's limaInstance.
func Instance(name string) (info Info, exists bool, err error) {
	var out bytes.Buffer
	cmd := exec.Command("limactl", "list", "--json", name)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return Info{}, false, fmt.Errorf("limactl list %s: %w", name, err)
	}
	// An absent instance yields empty output (no JSON line) rather than an error.
	if strings.TrimSpace(out.String()) == "" {
		return Info{}, false, nil
	}
	var j limaJSON
	if err := json.NewDecoder(&out).Decode(&j); err != nil {
		return Info{}, false, nil
	}
	return Info{Status: j.Status, Dir: j.Dir, SSHLocalPort: j.SSHLocalPort}, true, nil
}

// SSHDetails reads the instance's generated ssh.config for the connection details Popo
// dials with — Hostname/User/IdentityFile — falling back to the same defaults the
// functional harness uses (host 127.0.0.1, current user, ~/.lima/_config/user identity),
// and the forwarded SSHLocalPort for the port.
func SSHDetails(name string) (host string, port int, usr, identity string, err error) {
	info, exists, err := Instance(name)
	if err != nil {
		return "", 0, "", "", err
	}
	if !exists {
		return "", 0, "", "", fmt.Errorf("lima instance %q not found", name)
	}
	host = sshConfigField(info.Dir, "Hostname")
	if host == "" {
		host = "127.0.0.1"
	}
	usr = sshConfigField(info.Dir, "User")
	if usr == "" {
		if u, uerr := user.Current(); uerr == nil {
			usr = u.Username
		}
	}
	identity = sshConfigField(info.Dir, "IdentityFile")
	if identity == "" {
		home, _ := os.UserHomeDir()
		identity = filepath.Join(home, ".lima", "_config", "user")
	}
	return host, info.SSHLocalPort, usr, identity, nil
}

// sshConfigField reads a single value from the instance's generated ssh.config (first
// matching directive wins, matching ssh's own precedence). Mirrors the functional
// harness's sshConfigField exactly so the two can't drift.
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

// Start brings up a new instance from templatePath, named name, non-interactively.
func Start(ctx context.Context, name, templatePath string) error {
	return run(ctx, "start", "--name", name, "--tty=false", templatePath)
}

// Stop stops a running instance.
func Stop(ctx context.Context, name string) error {
	return run(ctx, "stop", name)
}

// Delete force-removes an instance (stopping it first if needed).
func Delete(ctx context.Context, name string) error {
	return run(ctx, "delete", "--force", name)
}

// Shell runs a command inside the instance over `limactl shell`, inheriting the parent's
// stdio. Used to apply the egress firewall (sudo sh -c <airgap.sh>) and any other
// bootstrap-time in-guest step.
func Shell(ctx context.Context, name string, args ...string) error {
	full := append([]string{"shell", name}, args...)
	cmd := exec.CommandContext(ctx, "limactl", full...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("limactl shell %s: %w", name, err)
	}
	return nil
}

// KnownHostsFile records the instance's SSH host key into <dir>/known_hosts via
// ssh-keyscan, returning the file path. Retries a few times (ssh-keyscan transiently
// fails under connection churn / while the box is still settling). Mirrors the functional
// harness's keyscanToFile, but writes to a stable per-sandbox path rather than a temp file.
func KnownHostsFile(name, dir string) (string, error) {
	_, port, _, _, err := SSHDetails(name)
	if err != nil {
		return "", err
	}
	host, hport := "127.0.0.1", port
	if h := hostFor(name); h != "" {
		host = h
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "known_hosts")

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		var out bytes.Buffer
		cmd := exec.Command("ssh-keyscan", "-T", "10", "-p", strconv.Itoa(hport), host)
		cmd.Stdout = &out // keys to stdout; progress noise goes to stderr
		err := cmd.Run()
		if err == nil && out.Len() > 0 {
			if werr := os.WriteFile(path, out.Bytes(), 0o644); werr != nil {
				return "", werr
			}
			return path, nil
		}
		lastErr = fmt.Errorf("ssh-keyscan %s:%d: %v", host, hport, err)
		time.Sleep(500 * time.Millisecond)
	}
	return "", lastErr
}

// hostFor returns the ssh.config Hostname for an instance, or "" when unavailable —
// keyscan then defaults to 127.0.0.1.
func hostFor(name string) string {
	info, exists, err := Instance(name)
	if err != nil || !exists {
		return ""
	}
	return sshConfigField(info.Dir, "Hostname")
}

// run executes `limactl <args...>` with inherited stderr (limactl's progress is chatty and
// useful during a create), capturing nothing — the caller reports success/failure.
func run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "limactl", args...)
	cmd.Stdout = os.Stderr // limactl prints progress to stdout; keep the CLI's stdout clean
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("limactl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
