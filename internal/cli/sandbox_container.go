package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/container"
	"github.com/KuroKodaNinja/iceclimber/internal/lima"
	"github.com/KuroKodaNinja/iceclimber/internal/xdg"
)

// The container backend (decision #111): a sandbox is a podman container on an INTERNAL
// (no-NAT) bridge — a host-netfilter air-gap that guest-root cannot undo. The Linux host
// is native on Linux, or ONE shared Lima VM on macOS. Popo dials Mac→Lima→container via a
// generated ssh_config (ProxyJump); the SSH/SFTP transport and `ssh -R` egress carry over.
const (
	containerHostInstance = "iceclimber-host"   // the shared Linux host VM (macOS)
	containerNetwork      = "iceclimber-airgap" // the internal no-NAT bridge
	containerBaseImage    = "docker.io/library/ubuntu:24.04"
)

// runnerForProvider builds the command Runner for a container sandbox: into the shared
// Lima VM on macOS, or local exec on native Linux.
func runnerForProvider(p *config.Provider) container.Runner {
	if p != nil && p.Host == "lima" && p.HostInstance != "" {
		return container.LimaRunner{Instance: p.HostInstance}
	}
	return container.LocalRunner{}
}

// hostKindForOS reports where the Linux container host lives on this controller.
func hostKindForOS() string {
	if runtime.GOOS == "linux" {
		return "native"
	}
	return "lima"
}

// ensureContainerHost makes sure a Linux host with podman is available, returning the
// Runner to drive it. On macOS it brings up (or reuses) the shared Lima VM and installs
// podman; on native Linux it just verifies podman is present.
func ensureContainerHost(ctx context.Context, out io.Writer) (container.Runner, string, string, error) {
	kind := hostKindForOS()
	if kind == "native" {
		r := container.LocalRunner{}
		if !container.New(r).Available(ctx) {
			return nil, "", "", fmt.Errorf("podman not found — install podman (rootful) to use the container backend on Linux")
		}
		return r, "native", "", nil
	}

	// macOS: shared Lima VM as the Linux host. Start-or-create + start-if-stopped;
	// `limactl start --name X <tmpl>` is idempotent (creates when absent, starts when
	// stopped). lima.Instance reports an error for an absent instance on some limactl
	// versions, so key the decision off run-state, not the error.
	if !lima.Available() {
		return nil, "", "", fmt.Errorf("limactl not found on PATH — install Lima to host containers on macOS (https://lima-vm.io)")
	}
	info, exists, ierr := lima.Instance(containerHostInstance)
	if ierr != nil || !exists || info.Status != "Running" {
		fmt.Fprintf(out, "  bringing up the shared container host VM %q…\n", containerHostInstance)
		tmpl, err := writeTemplate(4, 6, 30) // a larger box: it hosts N containers
		if err != nil {
			return nil, "", "", err
		}
		defer os.Remove(tmpl)
		if err := lima.Start(ctx, containerHostInstance, tmpl); err != nil {
			return nil, "", "", err
		}
	}
	r := container.LimaRunner{Instance: containerHostInstance}
	if !container.New(r).Available(ctx) {
		fmt.Fprintln(out, "  installing podman (netavark backend) in the host VM…")
		// netavark (not the CNI default): CNI's `--internal` leaves the bridge without a
		// host-side gateway IP and its portmap is flaky, so host→container reachability is
		// unreliable. netavark assigns a gateway and forwards published ports reliably.
		// Installing + configuring it BEFORE the first `podman network create` makes podman
		// pick it (validated in analysis/airgap-backends-spec.md).
		install := "export DEBIAN_FRONTEND=noninteractive; apt-get update -qq && " +
			"apt-get install -y -qq --no-install-recommends podman netavark aardvark-dns >/dev/null && " +
			"mkdir -p /etc/containers && " +
			`printf '[network]\nnetwork_backend = "netavark"\n' > /etc/containers/containers.conf`
		if err := lima.RunScript(ctx, containerHostInstance, install); err != nil {
			return nil, "", "", fmt.Errorf("install podman in host VM: %w", err)
		}
		if !container.New(r).Available(ctx) {
			return nil, "", "", fmt.Errorf("podman still unavailable in host VM after install")
		}
	}
	return r, "lima", containerHostInstance, nil
}

// createContainerSandbox is the container-backend path of `sandbox create`.
func createContainerSandbox(ctx context.Context, out io.Writer, name, isolation string) (*config.Config, error) {
	r, hostKind, hostInstance, err := ensureContainerHost(ctx, out)
	if err != nil {
		return nil, err
	}
	cl := container.New(r)

	// isolation-strength dial: default runc/crun, or gVisor's runsc.
	runtimePath := ""
	if isolation == "gvisor" {
		runtimePath = "/usr/local/bin/runsc"
	}

	fmt.Fprintln(out, "  ensuring the internal (no-NAT) air-gap network…")
	if err := cl.EnsureNetwork(ctx, containerNetwork); err != nil {
		return nil, fmt.Errorf("ensure network: %w", err)
	}
	fmt.Fprintln(out, "  ensuring the sandbox base image (sshd, non-sudo agent)…")
	if err := cl.EnsureImage(ctx, container.DefaultImage, containerBaseImage); err != nil {
		return nil, fmt.Errorf("ensure image: %w", err)
	}

	sbDir := xdg.SandboxDir(name)
	if err := os.MkdirAll(sbDir, 0o755); err != nil {
		return nil, err
	}
	// per-sandbox control keypair; the pubkey is authorized in the container.
	priv, pub, err := genSandboxKeypair(sbDir)
	if err != nil {
		return nil, fmt.Errorf("generate sandbox key: %w", err)
	}

	cname := "iceclimber-" + name
	fmt.Fprintf(out, "  launching container %q on the air-gapped network…\n", cname)
	if _, err := cl.Run(ctx, container.RunSpec{Name: cname, Network: containerNetwork, Runtime: runtimePath}); err != nil {
		return nil, fmt.Errorf("run container: %w", err)
	}
	// Connect as the non-sudo `agent` user (NOT root): the workload (Claude Code) refuses
	// --dangerously-skip-permissions as uid 0 (decision #110), and the air-gap is enforced
	// OUTSIDE the container (host netfilter) so it doesn't rely on an in-container non-root
	// user anyway.
	if err := cl.AuthorizeKey(ctx, cname, "agent", pub); err != nil {
		return nil, fmt.Errorf("authorize control key: %w", err)
	}
	// The controller dials the container at its bridge IP:22 (through the Lima VM via
	// ProxyJump on macOS); netavark gives the internal bridge a host-side gateway, so the
	// host/Lima VM can reach it.
	ip, err := cl.IP(ctx, cname)
	if err != nil {
		return nil, fmt.Errorf("resolve container IP: %w", err)
	}

	// record the container's host key (scanned from the host's vantage; also a readiness
	// gate for sshd) + write the ProxyJump ssh_config Popo dials through.
	known, err := writeContainerKnownHosts(ctx, cl, sbDir, ip)
	if err != nil {
		return nil, fmt.Errorf("record container host key: %w", err)
	}
	sshCfgPath, err := writeContainerSSHConfig(sbDir, name, ip, priv, known, hostKind, hostInstance)
	if err != nil {
		return nil, fmt.Errorf("write ssh config: %w", err)
	}

	cfg := newContainerConfig(name, cname, ip, priv, known, sshCfgPath, hostKind, hostInstance, isolation)
	if err := writeSandboxConfig(cfg); err != nil {
		return nil, err
	}
	fmt.Fprintf(out, "  registered %s\n", sandboxConfigPath(name))
	fmt.Fprintln(out, "  air-gap: internal no-NAT bridge (host-enforced; guest-root cannot egress)")
	return cfg, nil
}

// genSandboxKeypair writes an ed25519 keypair into sbDir via ssh-keygen and returns the
// private key path + the public key line.
func genSandboxKeypair(sbDir string) (privPath, pub string, err error) {
	privPath = filepath.Join(sbDir, "id_ed25519")
	_ = os.Remove(privPath)
	_ = os.Remove(privPath + ".pub")
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "iceclimber-sandbox", "-f", privPath)
	if outb, err := cmd.CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("ssh-keygen: %v: %s", err, strings.TrimSpace(string(outb)))
	}
	pb, err := os.ReadFile(privPath + ".pub")
	if err != nil {
		return "", "", err
	}
	return privPath, strings.TrimSpace(string(pb)), nil
}

// writeContainerKnownHosts scans the container's host key at its bridge IP:22 from the
// host's vantage (also a readiness gate for sshd) and writes the known_hosts file.
func writeContainerKnownHosts(ctx context.Context, cl *container.Client, sbDir, ip string) (string, error) {
	line, err := cl.Keyscan(ctx, ip, 22)
	if err != nil {
		return "", err
	}
	path := filepath.Join(sbDir, "known_hosts")
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// writeContainerSSHConfig generates the ssh_config Popo dials through: the sandbox sits at
// its bridge IP:22 inside the Linux host. On macOS a ProxyJump hop reaches it through the
// shared Lima VM; on native Linux the bridge IP is directly reachable.
func writeContainerSSHConfig(sbDir, name, ip, identity, known, hostKind, hostInstance string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# generated by `iceclimber sandbox create --provider container` — do not edit\n")
	jump := ""
	if hostKind == "lima" {
		host, jport, usr, jid, err := lima.SSHDetails(hostInstance)
		if err != nil {
			return "", fmt.Errorf("host VM ssh details: %w", err)
		}
		jump = "icx-jump-" + name
		fmt.Fprintf(&b, "Host %s\n  HostName %s\n  Port %d\n  User %s\n  IdentityFile %s\n"+
			"  StrictHostKeyChecking no\n  UserKnownHostsFile /dev/null\n\n",
			jump, host, jport, usr, jid)
	}
	fmt.Fprintf(&b, "Host %s\n  HostName %s\n  Port 22\n  User agent\n  IdentityFile %s\n"+
		"  UserKnownHostsFile %s\n  StrictHostKeyChecking accept-new\n", name, ip, identity, known)
	if jump != "" {
		fmt.Fprintf(&b, "  ProxyJump %s\n", jump)
	}
	path := filepath.Join(sbDir, "ssh_config")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// newContainerConfig builds the registry config.Config for a container sandbox: the SSH
// block dials the sandbox alias through the generated ssh_config (ProxyJump-aware), and
// the provider block records the container backend.
func newContainerConfig(name, cname, ip, identity, known, sshCfg, hostKind, hostInstance, isolation string) *config.Config {
	useCfg := true
	if isolation == "" {
		isolation = "runc"
	}
	return &config.Config{
		SandboxID:  name,
		RemoteRoot: "/home/agent/.iceclimber", // the non-sudo agent user's home
		SSH: config.SSH{
			Host:          name, // the alias resolved via the generated ssh_config
			Port:          22,
			User:          "agent",
			IdentityFile:  identity,
			KnownHosts:    known,
			UseSSHConfig:  &useCfg,
			SSHConfigFile: sshCfg,
		},
		EgressMode: "proxy",
		Provider: &config.Provider{
			Type:         "container",
			Instance:     cname,
			Distro:       "ubuntu",
			Arch:         hostArch(),
			Host:         hostKind,
			HostInstance: hostInstance,
			Network:      containerNetwork,
			Isolation:    isolation,
		},
	}
}

// containerProviderStatus reports the container's run state for list/status (mirrors the
// lima.Instance role for the container backend), normalized to a Lima-ish label.
func containerProviderStatus(ctx context.Context, p *config.Provider) (status string, exists bool) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	st, ok := container.New(runnerForProvider(p)).State(ctx, p.Instance)
	if !ok {
		return "", false
	}
	if st == "running" {
		return "Running", true
	}
	return strings.ToUpper(st[:1]) + st[1:], true
}

// containerDelete removes the sandbox container (NOT the shared host VM — it may back
// other sandboxes). The host VM is torn down separately.
func containerDelete(ctx context.Context, p *config.Provider) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return container.New(runnerForProvider(p)).Remove(ctx, p.Instance)
}

// isContainerProvider reports whether cfg is a container-backed sandbox.
func isContainerProvider(p *config.Provider) bool { return p != nil && p.Type == "container" }
