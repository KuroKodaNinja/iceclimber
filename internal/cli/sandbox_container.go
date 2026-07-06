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
	"github.com/spf13/cobra"
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

	// isolation-strength dial: default runc/crun, or gVisor's runsc (installed on demand).
	runtimePath := ""
	if isolation == "gvisor" {
		fmt.Fprintln(out, "  ensuring the gVisor runtime (runsc) in the host…")
		if err := cl.EnsureRunsc(ctx); err != nil {
			return nil, err
		}
		runtimePath = container.RunscPath
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

// atomicWriteFile writes data to path via a temp file in the same directory + rename, so a
// concurrent reader — an ssh/sftp dial that runs refreshContainerSSH while another process is
// rewriting the shared ssh_config/known_hosts after a container restart — never observes a
// truncated or half-written file. The rename is atomic on the same filesystem.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // harmless no-op once the rename succeeds
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// writeContainerKnownHosts scans the container's host key at its bridge IP:22 from the
// host's vantage (also a readiness gate for sshd) and writes the known_hosts file.
func writeContainerKnownHosts(ctx context.Context, cl *container.Client, sbDir, ip string) (string, error) {
	line, err := cl.Keyscan(ctx, ip, 22)
	if err != nil {
		return "", err
	}
	path := filepath.Join(sbDir, "known_hosts")
	if err := atomicWriteFile(path, []byte(line), 0o644); err != nil {
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
	if err := atomicWriteFile(path, []byte(b.String()), 0o600); err != nil {
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

// rewriteKnownHostsAddr points every known_hosts entry at addr, keeping the key type
// and key material (the pinned trust) intact.
func rewriteKnownHostsAddr(path, addr string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for i, l := range lines {
		f := strings.Fields(l)
		if len(f) >= 3 && !strings.HasPrefix(f[0], "#") {
			f[0] = addr
			lines[i] = strings.Join(f, " ")
		}
	}
	return atomicWriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
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

// newSandboxHostCmd manages the shared Linux host VM that backs container sandboxes on macOS
// (on native Linux there is no VM — containers run on the host directly).
func newSandboxHostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "Manage the shared Linux host VM that backs `--provider container` sandboxes (macOS)",
	}
	cmd.AddCommand(newSandboxHostStatusCmd(), newSandboxHostRmCmd())
	return cmd
}

func newSandboxHostStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the shared container host VM and what it backs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if hostKindForOS() == "native" {
				fmt.Fprintln(out, "container backend runs natively on this Linux host — no managed host VM")
				return nil
			}
			users := containerSandboxesUsingHost()
			info, exists, _ := lima.Instance(containerHostInstance)
			state := "absent"
			if exists {
				state = info.Status
			}
			fmt.Fprintf(out, "host VM:   %s — %s\n", containerHostInstance, state)
			fmt.Fprintf(out, "backing:   %d container sandbox(es)%s\n", len(users), parenNames(users))
			if exists && info.Status == "Running" {
				runner := container.LimaRunner{Instance: containerHostInstance}
				pod := "podman: unreachable"
				if container.New(runner).Available(cmd.Context()) {
					pod = "podman: ready"
				}
				runsc := "runsc: not installed"
				if _, err := runner.Run(cmd.Context(), container.RunscPath, "--version"); err == nil {
					runsc = "runsc: installed (gVisor tier ready)"
				}
				fmt.Fprintf(out, "runtime:   %s · %s\n", pod, runsc)
			}
			return nil
		},
	}
}

func newSandboxHostRmCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "rm",
		Short: "Destroy the shared container host VM (and every container on it)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if hostKindForOS() == "native" {
				fmt.Fprintln(out, "nothing to remove — the container backend runs natively (no host VM)")
				return nil
			}
			users := containerSandboxesUsingHost()
			if len(users) > 0 && !force {
				return fmt.Errorf("%d container sandbox(es) still use the host VM%s — remove them first (`sandbox rm <id>`), or pass --force to destroy them all",
					len(users), parenNames(users))
			}
			if _, exists, _ := lima.Instance(containerHostInstance); !exists {
				fmt.Fprintln(out, "host VM already absent")
				return nil
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
			defer cancel()
			if err := lima.Delete(ctx, containerHostInstance); err != nil {
				return fmt.Errorf("delete host VM %q: %w", containerHostInstance, err)
			}
			fmt.Fprintf(out, "destroyed host VM %q\n", containerHostInstance)
			if len(users) > 0 {
				fmt.Fprintf(out, "note: %d registry entry(ies) still reference the destroyed host%s — `sandbox rm` them to clean up\n",
					len(users), parenNames(users))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "destroy the host VM even if container sandboxes still use it")
	return cmd
}

// containerSandboxesUsingHost returns the ids of container sandboxes bound to the shared host VM.
func containerSandboxesUsingHost() []string {
	var ids []string
	for _, e := range listSandboxes() {
		if e.Cfg != nil && isContainerProvider(e.Cfg.Provider) && e.Cfg.Provider.HostInstance == containerHostInstance {
			ids = append(ids, e.ID)
		}
	}
	return ids
}

// parenNames renders a name list as " (a, b)" or "" when empty.
func parenNames(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return " (" + strings.Join(ids, ", ") + ")"
}

// refreshContainerSSH makes a container sandbox reachable after a possible restart: a host
// reboot leaves the container STOPPED, and on restart podman may hand it a NEW bridge IP,
// making the stored ssh_config stale. So before every dial we start the container if stopped,
// re-resolve its IP, and rewrite the ssh_config + known_hosts if the IP moved. A no-op for
// non-container providers (and cheap when nothing changed). Called from openSessionWith.
func refreshContainerSSH(ctx context.Context, cfg *config.Config) error {
	if !isContainerProvider(cfg.Provider) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return refreshContainerSSHWith(ctx, cfg, container.New(runnerForProvider(cfg.Provider)))
}

// refreshContainerSSHWith is refreshContainerSSH over an injected client (the unit-test
// seam — tests drive it with a fake Runner).
func refreshContainerSSHWith(ctx context.Context, cfg *config.Config, cl *container.Client) error {
	p := cfg.Provider
	st, exists := cl.State(ctx, p.Instance)
	if !exists {
		// Don't report a dead context as a missing container — the message would send
		// the operator to recreate a sandbox that is fine.
		if ctx.Err() != nil {
			return fmt.Errorf("container refresh aborted: %w", ctx.Err())
		}
		return fmt.Errorf("sandbox container %q not found on its host — was it removed?", p.Instance)
	}
	started := false
	if st != "running" {
		if err := cl.Start(ctx, p.Instance); err != nil {
			return fmt.Errorf("start sandbox container %q: %w", p.Instance, err)
		}
		started = true
	}
	ip, err := cl.IP(ctx, p.Instance)
	if err != nil {
		return err
	}
	if !containerSSHConfigHasIP(cfg.SSH.SSHConfigFile, ip) {
		// The endpoint moved: re-point the generated ssh_config + known_hosts at the
		// current IP, PRESERVING the pinned host key material — the container's
		// filesystem (and thus its sshd host key) survives stop/start, so trust is
		// never re-scanned from the wire; a different container squatting the address
		// must still fail strict host-key checking.
		if err := rewriteKnownHostsAddr(cfg.SSH.KnownHosts, ip); err != nil {
			return fmt.Errorf("rewrite known_hosts: %w", err)
		}
		sbDir := xdg.SandboxDir(cfg.SandboxID)
		if _, err := writeContainerSSHConfig(sbDir, cfg.SandboxID, ip, cfg.SSH.IdentityFile, cfg.SSH.KnownHosts, p.Host, p.HostInstance); err != nil {
			return fmt.Errorf("rewrite ssh config: %w", err)
		}
	}
	if started {
		// Readiness gate for the just-started sshd (it may still be binding, and the
		// dial that follows has no retry) — the scan output is DISCARDED; trust stays
		// pinned in the preserved known_hosts.
		if _, err := cl.Keyscan(ctx, ip, 22); err != nil {
			return fmt.Errorf("container %q sshd not reachable at %s: %w", p.Instance, ip, err)
		}
	}
	return nil
}

// containerSSHConfigHasIP reports whether the generated ssh_config already targets ip on the
// container's HostName line (the jump line is a different address), so a restart-rewrite is
// only done when the IP actually changed.
func containerSSHConfigHasIP(sshCfgPath, ip string) bool {
	data, err := os.ReadFile(sshCfgPath)
	return err == nil && strings.Contains(string(data), "HostName "+ip+"\n")
}
