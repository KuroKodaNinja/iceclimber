// Package container runs an iceclimber sandbox as a CONTAINER on an INTERNAL
// (no-NAT) bridge — a host-netfilter-enforced, guest-root-proof air-gap (decision
// #111, validated by the analysis/ spike): even a privileged container-root cannot
// egress, because the DROP lives in the host network namespace, outside the container.
//
// It is a thin wrapper over the `podman` CLI (rootful — the host-netfilter air-gap
// needs netavark, not rootless pasta/slirp), mirroring internal/lima's shape. The
// commands run on a Linux HOST, reached through a Runner: locally on native Linux, or
// via `limactl shell` into the shared Lima VM on macOS. That Runner is the whole
// cross-platform reuse seam — the container logic here is identical on both; only how
// a command reaches the Linux host differs (so on Linux the VM is omitted entirely).
package container

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// provisionScript builds the sandbox base image's contents (sshd with TCP forwarding
// on — required for the `ssh -R` egress tunnel — a non-sudo `agent` workload user, and
// host keys). Embedded so the driver carries it without a build context.
//
//go:embed provision.sh
var provisionScript string

// DefaultImage is the committed sandbox base image tag.
const DefaultImage = "iceclimber-sandbox:base"

// Runner executes a command on the Linux host that owns the containers. Combined
// stdout is returned; on failure the error carries stderr. This is the platform seam:
// LocalRunner on native Linux, LimaRunner (limactl shell) on macOS.
type Runner interface {
	Run(ctx context.Context, argv ...string) (string, error)
}

// LocalRunner runs commands directly on the host (native Linux).
type LocalRunner struct{}

func (LocalRunner) Run(ctx context.Context, argv ...string) (string, error) {
	return runArgv(ctx, false, "", argv)
}

// LimaRunner runs commands inside a Lima instance via `limactl shell` (macOS host).
// Stdin is never inherited and WaitDelay is bounded so a background/non-tty caller
// can't hang on a mux'd ssh child (the same lesson as internal/lima.RunScript).
type LimaRunner struct{ Instance string }

func (l LimaRunner) Run(ctx context.Context, argv ...string) (string, error) {
	return runArgv(ctx, true, l.Instance, argv)
}

func runArgv(ctx context.Context, lima bool, instance string, argv []string) (string, error) {
	var full []string
	if lima {
		full = append([]string{"limactl", "shell", instance, "--"}, argv...)
	} else {
		full = argv
	}
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.Stdin = nil
	cmd.WaitDelay = 5 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s: %w: %s", strings.Join(argv, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Client issues podman verbs through a Runner. base is the command prefix — rootful
// podman, e.g. ["sudo","podman"] on the Lima VM (its default user has passwordless
// sudo) or ["podman"] when iceclimber already runs as root on native Linux.
type Client struct {
	r    Runner
	base []string
}

// New builds a Client. base defaults to ["sudo","podman"] (the validated rootful path);
// pass an override for a root-native host, e.g. New(r, "podman").
func New(r Runner, base ...string) *Client {
	if len(base) == 0 {
		base = []string{"sudo", "podman"}
	}
	return &Client{r: r, base: base}
}

// podman runs `<base> <args...>` on the host and returns trimmed stdout.
func (c *Client) podman(ctx context.Context, args ...string) (string, error) {
	out, err := c.r.Run(ctx, append(append([]string{}, c.base...), args...)...)
	return strings.TrimSpace(out), err
}

// Available reports whether podman is usable on the host (also proves the Runner works,
// e.g. the Lima VM is reachable).
func (c *Client) Available(ctx context.Context) bool {
	_, err := c.podman(ctx, "version", "--format", "{{.Client.Version}}")
	return err == nil
}

// EnsureNetwork idempotently creates an INTERNAL (no-NAT) bridge network. This is the
// air-gap primitive: containers on it reach the host + peers but have no route to the
// internet, enforced by host netfilter (guest-root-proof).
func (c *Client) EnsureNetwork(ctx context.Context, name string) error {
	if _, err := c.podman(ctx, "network", "inspect", name); err == nil {
		return nil // already present
	}
	_, err := c.podman(ctx, "network", "create", "--internal", name)
	return err
}

// RemoveNetwork deletes the network (force — detaches any lingering endpoints).
func (c *Client) RemoveNetwork(ctx context.Context, name string) error {
	_, err := c.podman(ctx, "network", "rm", "-f", name)
	return err
}

// ImageExists reports whether the given image tag is present on the host.
func (c *Client) ImageExists(ctx context.Context, image string) bool {
	_, err := c.podman(ctx, "image", "exists", image)
	return err == nil
}

// EnsureImage builds the sandbox base image if absent: run the base (on the DEFAULT
// network, which has internet) through provision.sh, then commit. Idempotent.
func (c *Client) EnsureImage(ctx context.Context, image, base string) error {
	if c.ImageExists(ctx, image) {
		return nil
	}
	tmp := "iceclimber-build-" + sanitize(image)
	_, _ = c.podman(ctx, "rm", "-f", tmp) // clear any stale builder
	// Provision on the default network (needs internet to apt-install sshd). Pin public
	// DNS: the Lima host's resolv.conf is the 127.0.0.53 systemd-resolved stub, which
	// netavark copies into the container where it isn't reachable — apt then fails to
	// resolve. The build container has NAT'd internet, so public resolvers work directly.
	if _, err := c.podman(ctx, "run", "--name", tmp, "--dns=1.1.1.1", "--dns=8.8.8.8", base, "sh", "-c", provisionScript); err != nil {
		_, _ = c.podman(ctx, "rm", "-f", tmp)
		return fmt.Errorf("provision base image: %w", err)
	}
	if _, err := c.podman(ctx, "commit", "-q", tmp, image); err != nil {
		_, _ = c.podman(ctx, "rm", "-f", tmp)
		return fmt.Errorf("commit base image: %w", err)
	}
	_, err := c.podman(ctx, "rm", "-f", tmp)
	return err
}

// RunSpec configures a sandbox container.
type RunSpec struct {
	Name    string // container name (the sandbox id-derived name)
	Image   string // base image (DefaultImage if empty)
	Network string // the internal network (must exist)
	Runtime string // OCI runtime path; "" = default (runc/crun); e.g. runsc for gVisor
}

// Run starts a detached sandbox container running sshd on the internal (no-NAT) network.
// The controller reaches it at the container's bridge IP:22 (the host — or the Lima VM —
// can dial it because netavark assigns the internal bridge a host-side gateway). sshd
// re-reads authorized_keys per connection, so the caller injects the control key afterward
// via AuthorizeKey. Returns the container id.
func (c *Client) Run(ctx context.Context, spec RunSpec) (id string, err error) {
	if spec.Image == "" {
		spec.Image = DefaultImage
	}
	args := []string{"run", "-d", "--name", spec.Name, "--network", spec.Network}
	if spec.Runtime != "" {
		args = append(args, "--runtime", spec.Runtime)
	}
	// sshd -D -e: foreground, log to stderr (captured by `podman logs`).
	args = append(args, spec.Image, "/usr/sbin/sshd", "-D", "-e")
	return c.podman(ctx, args...)
}

// AuthorizeKey installs an SSH public key for a user in the running container so Popo
// (or the Lima host, via ProxyJump) can dial in. user "" defaults to root.
func (c *Client) AuthorizeKey(ctx context.Context, name, user, pubKey string) error {
	if user == "" {
		user = "root"
	}
	home := "/root"
	if user != "root" {
		home = "/home/" + user
	}
	script := fmt.Sprintf(
		"mkdir -p %s/.ssh && printf '%%s\\n' %s >> %s/.ssh/authorized_keys && "+
			"chown -R %s %s/.ssh && chmod 700 %s/.ssh && chmod 600 %s/.ssh/authorized_keys",
		home, shSingleQuote(pubKey), home, user, home, home, home)
	_, err := c.Exec(ctx, name, "sh", "-c", script)
	return err
}

// IP resolves the container's address on its network. It is NOT stable across restart,
// so callers resolve it dynamically rather than caching (a spike finding).
func (c *Client) IP(ctx context.Context, name string) (string, error) {
	out, err := c.podman(ctx, "inspect", name, "--format",
		"{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(out)
	if ip == "" {
		return "", fmt.Errorf("container %q has no network IP yet", name)
	}
	return ip, nil
}

// Exec runs a command inside the container, returning trimmed stdout.
func (c *Client) Exec(ctx context.Context, name string, argv ...string) (string, error) {
	return c.podman(ctx, append([]string{"exec", name}, argv...)...)
}

// Keyscan reads the container's SSH host key from the host's vantage (host/Lima VM →
// container bridge IP), returning the known_hosts line(s). It doubles as a readiness gate:
// a freshly-launched container's sshd (esp. the first on a fresh host) can take a while to
// bind, so this polls for up to ~60s before giving up.
func (c *Client) Keyscan(ctx context.Context, host string, port int) (string, error) {
	var last error
	deadline := time.Now().Add(60 * time.Second)
	for attempt := 0; ; attempt++ {
		out, err := c.r.Run(ctx, "ssh-keyscan", "-T", "3", "-p", strconv.Itoa(port), host)
		if err == nil && strings.TrimSpace(out) != "" {
			return out, nil
		}
		last = fmt.Errorf("ssh-keyscan %s:%d: %v", host, port, err)
		if time.Now().After(deadline) || ctx.Err() != nil {
			return "", last
		}
		time.Sleep(2 * time.Second)
	}
}

// State returns the container's run status ("running", "exited", …) and whether it
// exists at all — the container-backend analogue of lima.Instance for list/status.
func (c *Client) State(ctx context.Context, name string) (status string, exists bool) {
	out, err := c.podman(ctx, "inspect", name, "--format", "{{.State.Status}}")
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(out)
	return s, s != ""
}

// Stop stops a running sandbox container.
func (c *Client) Stop(ctx context.Context, name string) error {
	_, err := c.podman(ctx, "stop", "-t", "5", name)
	return err
}

// Remove force-removes a sandbox container (stopping it first).
func (c *Client) Remove(ctx context.Context, name string) error {
	_, err := c.podman(ctx, "rm", "-f", name)
	return err
}

// sanitize turns an image tag into a safe container name fragment.
func sanitize(s string) string {
	return strings.NewReplacer(":", "-", "/", "-", ".", "-").Replace(s)
}

// shSingleQuote wraps s in single quotes for safe embedding in an sh -c string.
func shSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
