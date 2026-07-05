package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/lima"
	"github.com/KuroKodaNinja/iceclimber/internal/xdg"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// newSandboxCmd is the `iceclimber sandbox` command group (§18): the multi-sandbox
// MANAGEMENT surface — create (Lima), list, status, shell (auto-serve), rm — over the
// per-sandbox registry (~/.config/iceclimber/<id>/) with a default pointer. Local
// (iceclimber-managed, provider: lima) and remote (hand-authored, no provider) sandboxes
// are both first-class; rm never destroys a box iceclimber didn't create.
func newSandboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage sandboxes: create (Lima), list, status, shell, rm (§18)",
		Long: "The multi-sandbox management surface. `sandbox create` provisions a local Lima VM\n" +
			"and registers it; list/status/shell/rm operate on every sandbox in the registry —\n" +
			"local and remote alike. iceclimber stays a bridge, not a VM manager: the value is\n" +
			"the registry + auto-bootstrap + jump-in, not the VM.",
	}
	cmd.AddCommand(
		newSandboxCreateCmd(),
		newSandboxListCmd(),
		newSandboxStatusCmd(),
		newSandboxShellCmd(),
		newSandboxRmCmd(),
	)
	return cmd
}

// ---------------------------------------------------------------------------
// Registry helpers (§18.1): the per-sandbox dir + the default pointer.
// ---------------------------------------------------------------------------

// sandboxConfigPath is the config.yaml for sandbox id under the registry.
func sandboxConfigPath(id string) string {
	return filepath.Join(xdg.SandboxDir(id), "config.yaml")
}

// defaultPointerPath is the ~/.config/iceclimber/default file naming the default sandbox.
func defaultPointerPath() string { return filepath.Join(xdg.Config(), "default") }

// readDefaultSandbox returns the id in the default pointer, or "" when unset/unreadable.
func readDefaultSandbox() string {
	data, err := os.ReadFile(defaultPointerPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// writeDefaultSandbox records id as the default sandbox.
func writeDefaultSandbox(id string) error {
	if err := os.MkdirAll(xdg.Config(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(defaultPointerPath(), []byte(id+"\n"), 0o644)
}

// clearDefaultSandbox removes the default pointer (best-effort).
func clearDefaultSandbox() { _ = os.Remove(defaultPointerPath()) }

// sandboxEntry is one registered sandbox: its id (== dir name), the loaded config (nil on
// a load error), and the config path. LoadErr is set when config.yaml failed to parse/validate.
type sandboxEntry struct {
	ID         string
	ConfigPath string
	Cfg        *config.Config
	LoadErr    error
}

// managed reports whether this is an iceclimber-managed local sandbox (has a provider block).
func (e sandboxEntry) managed() bool { return e.Cfg != nil && e.Cfg.Provider != nil }

// listSandboxes scans ~/.config/iceclimber/*/config.yaml — the directory of per-sandbox dirs
// IS the sandbox list. A subdir without a config.yaml is not a sandbox (skipped). Entries
// are returned sorted by id; a config that fails to load is still listed (with LoadErr) so a
// broken box is visible rather than silently dropped.
func listSandboxes() []sandboxEntry {
	root := xdg.Config()
	dirs, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []sandboxEntry
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		id := d.Name()
		cfgPath := sandboxConfigPath(id)
		if _, err := os.Stat(cfgPath); err != nil {
			continue // no config.yaml → not a sandbox
		}
		e := sandboxEntry{ID: id, ConfigPath: cfgPath}
		cfg, lerr := config.Load(cfgPath, id)
		if lerr != nil {
			e.LoadErr = lerr
		} else {
			e.Cfg = cfg
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// defaultSandbox resolves the sandbox id a subcommand acts on when no name arg is given:
// the explicit --sandbox global, else the default pointer. "" means neither is set.
func defaultSandbox() string {
	if sandboxID != "" {
		return sandboxID
	}
	return readDefaultSandbox()
}

// nextCreateName picks the default `create` name: "dev" if free, else "dev-2", "dev-3", …
// against the current registry — so repeated `sandbox create` (no name) never collides.
func nextCreateName() string {
	taken := map[string]bool{}
	for _, e := range listSandboxes() {
		taken[e.ID] = true
	}
	if !taken["dev"] {
		return "dev"
	}
	for n := 2; ; n++ {
		cand := "dev-" + strconv.Itoa(n)
		if !taken[cand] {
			return cand
		}
	}
}

// validSandboxName rejects ids that would break the registry (path separators, the reserved
// fleet selector "all", the pointer filename "default", or empty). id must equal its dir name.
func validSandboxName(id string) error {
	switch {
	case id == "":
		return fmt.Errorf("sandbox name must not be empty")
	case strings.ContainsAny(id, "/\\"):
		return fmt.Errorf("sandbox name %q must not contain a path separator", id)
	case id == "all":
		return fmt.Errorf("sandbox name %q is reserved (the fleet selector)", id)
	case id == "default":
		return fmt.Errorf("sandbox name %q is reserved (the default pointer)", id)
	case strings.HasPrefix(id, "."):
		return fmt.Errorf("sandbox name %q must not start with '.'", id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// create (§18.2)
// ---------------------------------------------------------------------------

func newSandboxCreateCmd() *cobra.Command {
	var (
		distro      string
		provider    string
		isolation   string
		cpus        int
		memory      int
		disk        int
		agentName   string
		dropShell   bool
		makeDefault bool
	)
	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Provision a local Lima sandbox and register it (default name: dev)",
		Long: "Brings up an Ubuntu (vz) Lima VM, records its SSH details + host key into the\n" +
			"registry, applies the egress air-gap, and bootstraps it (protocol tree + popo,\n" +
			"proxy egress). With --shell it drops you in with an auto-serve for the session.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.ToLower(distro) != "ubuntu" {
				return fmt.Errorf("--distro %q not supported yet (only 'ubuntu' is wired; alpine is deferred, §18.5)", distro)
			}
			if provider != "lima" && provider != "container" {
				return fmt.Errorf("--provider %q not supported (use 'lima' or 'container')", provider)
			}
			name := nextCreateName()
			if len(args) == 1 {
				name = args[0]
			}
			if err := validSandboxName(name); err != nil {
				return err
			}
			if _, err := os.Stat(sandboxConfigPath(name)); err == nil {
				return fmt.Errorf("sandbox %q already exists (%s) — pick another name or `sandbox rm %s` first", name, sandboxConfigPath(name), name)
			}
			out := cmd.OutOrStdout()

			// Container backend (decision #111): a podman container on an internal no-NAT
			// bridge — host-enforced, guest-root-proof air-gap; hosted natively on Linux or
			// in a shared Lima VM on macOS. The SSH/SFTP transport + ssh -R egress carry over.
			if provider == "container" {
				ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Minute)
				defer cancel()
				fmt.Fprintf(out, "creating sandbox %q (container backend, isolation=%s)…\n", name, isolationOrDefault(isolation))
				cfg, err := createContainerSandbox(ctx, out, name, isolation)
				if err != nil {
					return err
				}
				if err := bootstrapSandbox(ctx, cfg); err != nil {
					return fmt.Errorf("bootstrap: %w", err)
				}
				fmt.Fprintln(out, "  bootstrapped (protocol tree + popo, proxy trust)")
				return finishCreate(cmd, cfg, dropShell, makeDefault, agentName)
			}

			// Lima (vz) backend — the original per-VM sandbox.
			if !lima.Available() {
				return fmt.Errorf("limactl not found on PATH — install Lima to create a local sandbox (https://lima-vm.io)")
			}
			instance := "iceclimber-" + name

			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Minute)
			defer cancel()

			// 1. Start the Lima VM from the embedded, resource-substituted template.
			tmplPath, err := writeTemplate(cpus, memory, disk)
			if err != nil {
				return err
			}
			defer os.Remove(tmplPath)
			fmt.Fprintf(out, "creating sandbox %q (lima instance %s, %d CPU / %d GiB / %d GiB)…\n", name, instance, cpus, memory, disk)
			if err := lima.Start(ctx, instance, tmplPath); err != nil {
				return err
			}

			// 2. Read SSH details + record the host key into the registry dir.
			host, port, usr, identity, err := lima.SSHDetails(instance)
			if err != nil {
				return fmt.Errorf("read SSH details for %s: %w", instance, err)
			}
			sbDir := xdg.SandboxDir(name)
			if err := os.MkdirAll(sbDir, 0o755); err != nil {
				return err
			}
			known, err := lima.KnownHostsFile(instance, sbDir)
			if err != nil {
				return fmt.Errorf("record host key for %s: %w", instance, err)
			}

			// 3. Write the registry config.yaml (marshal a config.Config).
			cfg := newManagedConfig(name, instance, distro, host, port, usr, identity, known)
			if err := writeSandboxConfig(cfg); err != nil {
				return err
			}
			fmt.Fprintf(out, "  registered %s\n", sandboxConfigPath(name))

			// 4. Apply the egress air-gap (the pluggable isolation SEAM, §18.3).
			if err := applyAirgap(ctx, instance); err != nil {
				return fmt.Errorf("apply air-gap: %w", err)
			}
			fmt.Fprintln(out, "  air-gap applied (egress rides Popo's reverse-tunnel proxy)")

			// 5. Bootstrap over the new config (reuses the same provision path as `bootstrap`).
			if err := bootstrapSandbox(ctx, cfg); err != nil {
				return fmt.Errorf("bootstrap: %w", err)
			}
			fmt.Fprintln(out, "  bootstrapped (protocol tree + popo, proxy trust)")

			return finishCreate(cmd, cfg, dropShell, makeDefault, agentName)
		},
	}
	cmd.Flags().StringVar(&distro, "distro", "ubuntu", "guest distro (only 'ubuntu' is wired today)")
	cmd.Flags().StringVar(&provider, "provider", "lima", "isolation backend: 'lima' (per-VM) or 'container' (podman on an internal no-NAT bridge — VM-external air-gap, decision #111)")
	cmd.Flags().StringVar(&isolation, "isolation", "runc", "container isolation-strength dial: 'runc' (default) or 'gvisor' (stronger, userspace kernel) — container backend only")
	cmd.Flags().IntVar(&cpus, "cpus", 2, "vCPUs for the VM")
	cmd.Flags().IntVar(&memory, "memory", 4, "memory in GiB")
	cmd.Flags().IntVar(&disk, "disk", 20, "disk in GiB")
	cmd.Flags().StringVar(&agentName, "agent", "", "coding agent to install after create (deferred — run `agent install` for now)")
	cmd.Flags().BoolVar(&dropShell, "shell", false, "drop into a shell after create, with auto-serve for the session")
	cmd.Flags().BoolVar(&makeDefault, "default", false, "set this sandbox as the default (no --sandbox) target")
	return cmd
}

// writeTemplate writes the embedded Ubuntu template to a temp file with the resource
// defaults substituted for the requested CPU/memory/disk, returning its path (the caller
// removes it). limactl start needs a file path, and this keeps the resource knobs in one place.
func writeTemplate(cpus, memoryGiB, diskGiB int) (string, error) {
	tmpl := string(lima.UbuntuTemplate)
	tmpl = strings.Replace(tmpl, "cpus: 2", fmt.Sprintf("cpus: %d", cpus), 1)
	tmpl = strings.Replace(tmpl, "memory: 4GiB", fmt.Sprintf("memory: %dGiB", memoryGiB), 1)
	tmpl = strings.Replace(tmpl, "disk: 20GiB", fmt.Sprintf("disk: %dGiB", diskGiB), 1)
	f, err := os.CreateTemp("", "iceclimber-lima-*.yaml")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(tmpl); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// newManagedConfig builds the config.Config `sandbox create` writes: the SSH block (direct
// dial, no ssh_config), proxy egress, and the provider block that marks this a managed box.
func newManagedConfig(name, instance, distro, host string, port int, usr, identity, known string) *config.Config {
	noSSHConfig := false
	return &config.Config{
		SandboxID: name,
		SSH: config.SSH{
			Host:         host,
			Port:         port,
			User:         usr,
			IdentityFile: identity,
			KnownHosts:   known,
			UseSSHConfig: &noSSHConfig,
		},
		EgressMode: "proxy",
		Provider: &config.Provider{
			Type:     "lima",
			Instance: instance,
			Distro:   distro,
			Arch:     hostArch(),
		},
	}
}

// finishCreate is the shared create tail for both backends: set the default pointer,
// note --agent, then either drop into an auto-serve shell or print next steps.
func finishCreate(cmd *cobra.Command, cfg *config.Config, dropShell, makeDefault bool, agentName string) error {
	out := cmd.OutOrStdout()
	name := cfg.SandboxID
	if makeDefault || len(listSandboxes()) == 1 {
		if err := writeDefaultSandbox(name); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not set default pointer: %v\n", err)
		} else {
			fmt.Fprintf(out, "  set as default sandbox\n")
		}
	}
	if agentName != "" {
		fmt.Fprintf(out, "  note: --agent is not wired into create yet — run `iceclimber agent install %s --sandbox %s`\n", agentName, name)
	}
	if dropShell {
		fmt.Fprintf(out, "  jumping in (auto-serve for the session)…\n")
		return autoServeShell(cmd, cfg, false)
	}
	fmt.Fprintf(out, "\nsandbox %q ready. Next:\n", name)
	fmt.Fprintf(out, "  iceclimber sandbox shell %s        # jump in (auto-serve)\n", name)
	fmt.Fprintf(out, "  iceclimber agent install claude --sandbox %s\n", name)
	fmt.Fprintf(out, "  iceclimber sandbox status %s\n", name)
	return nil
}

// isolationOrDefault renders the isolation tier for display ("" ⇒ "runc").
func isolationOrDefault(s string) string {
	if s == "" {
		return "runc"
	}
	return s
}

// writeSandboxConfig marshals cfg to its registry config.yaml.
func writeSandboxConfig(cfg *config.Config) error {
	if err := os.MkdirAll(xdg.SandboxDir(cfg.SandboxID), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(sandboxConfigPath(cfg.SandboxID), data, 0o600)
}

// hostArch maps the controller's GOARCH to the Lima/uname arch string the provider records.
func hostArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "aarch64"
	case "amd64":
		return "x86_64"
	default:
		return runtime.GOARCH
	}
}

// applyAirgap is the egress-isolation SEAM (§18.3): it applies the embedded firewall inside the
// guest as root, feeding the script on stdin (`sudo sh -s up`). NON-interactive (RunScript, not
// Shell) so create works from a background/CI/non-tty context — inheriting a dead os.Stdin got
// the ssh child SIGKILLed. Isolated as one clear function so a stronger (VM-external) mechanism
// can drop in here without touching create.
func applyAirgap(ctx context.Context, instance string) error {
	// Non-interactive (RunScript = `sudo sh -c`, no stdin) so create works headless AND the
	// firewall apply can't hang on a stdin-fed remote shell. Bounded so a stuck apply fails in
	// ~90s instead of blocking create indefinitely. airgap.sh defaults its action to "up".
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return lima.RunScript(ctx, instance, string(lima.AirgapScript))
}

// bootstrapSandbox provisions a freshly-created box: it opens the mutating-ops session and
// runs the SAME `provision` + `writeEgressTrust` steps the `bootstrap` command runs — not a
// reimplementation — so create and `iceclimber bootstrap` stay identical.
func bootstrapSandbox(ctx context.Context, cfg *config.Config) error {
	sess, err := openLockedSession(ctx, cfg, transport, "sandbox create bootstrap")
	if err != nil {
		return err
	}
	defer sess.Close()
	if err := provision(ctx, sess); err != nil {
		return err
	}
	return writeEgressTrust(ctx, sess, cfg)
}

// ---------------------------------------------------------------------------
// list (§18.1)
// ---------------------------------------------------------------------------

func newSandboxListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every registered sandbox (local + remote)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			entries := listSandboxes()
			def := readDefaultSandbox()
			w := cmd.OutOrStdout()

			if jsonOutput {
				rows := make([]sandboxListRow, 0, len(entries))
				for _, e := range entries {
					rows = append(rows, sandboxListRowFor(e, def))
				}
				return json.NewEncoder(w).Encode(rows)
			}

			if len(entries) == 0 {
				fmt.Fprintln(w, "no sandboxes registered — create one with `iceclimber sandbox create`")
				return nil
			}
			for _, e := range entries {
				r := sandboxListRowFor(e, def)
				star := " "
				if r.Default {
					star = "★"
				}
				fmt.Fprintf(w, "%s %-16s %-14s %-10s %s\n", star, r.Name, r.Provider, r.VMStatus, r.Reachable)
			}
			return nil
		},
	}
	return cmd
}

// sandboxListRow is one row of `sandbox list` (also the --json shape).
type sandboxListRow struct {
	Name      string `json:"name"`
	Default   bool   `json:"default"`
	Provider  string `json:"provider"`  // "lima:ubuntu" | "remote" | "invalid"
	VMStatus  string `json:"vm_status"` // lima status | "—" (remote) | "unknown"
	Reachable string `json:"reachable"` // "yes" | "no" | "?"
}

func sandboxListRowFor(e sandboxEntry, def string) sandboxListRow {
	r := sandboxListRow{Name: e.ID, Default: e.ID == def}
	if e.LoadErr != nil {
		r.Provider, r.VMStatus, r.Reachable = "invalid", "—", "?"
		return r
	}
	if e.managed() {
		p := e.Cfg.Provider
		r.Provider = p.Type + ":" + p.Distro
		r.VMStatus = "unknown"
		switch p.Type {
		case "container":
			if st, exists := containerProviderStatus(context.Background(), p); exists {
				r.VMStatus = st
			} else {
				r.VMStatus = "absent"
			}
		default: // lima
			if info, exists, err := lima.Instance(p.Instance); err == nil && exists {
				r.VMStatus = info.Status
			} else if err == nil {
				r.VMStatus = "absent"
			}
		}
	} else {
		r.Provider = "remote"
		r.VMStatus = "—"
	}
	r.Reachable = reachable(e.Cfg)
	return r
}

// reachable is a best-effort quick TCP probe of the sandbox's SSH endpoint (short timeout),
// so `list` can hint liveness without a full dial. "?" when the host/port aren't dialable.
func reachable(cfg *config.Config) string {
	host, port := cfg.SSH.Host, cfg.SSH.Port
	if host == "" || port == 0 {
		return "?" // ssh_config-resolved endpoints aren't cheap to probe here
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 700*time.Millisecond)
	if err != nil {
		return "no"
	}
	_ = conn.Close()
	return "yes"
}

// ---------------------------------------------------------------------------
// status (§18.2)
// ---------------------------------------------------------------------------

func newSandboxStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status [name]",
		Short: "Show a sandbox's VM status and a one-line iceclimber summary",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := firstNonEmpty(args, defaultSandbox())
			if id == "" {
				return fmt.Errorf("no sandbox given and no default set — name one or `sandbox create --default`")
			}
			cfg, err := config.Load(sandboxConfigPath(id), id)
			if err != nil {
				return fmt.Errorf("load sandbox %q: %w", id, err)
			}
			w := cmd.OutOrStdout()

			// VM/container line.
			if cfg.Provider != nil {
				vm := "unknown"
				switch cfg.Provider.Type {
				case "container":
					if st, exists := containerProviderStatus(cmd.Context(), cfg.Provider); exists {
						vm = st
					} else {
						vm = "absent"
					}
				default: // lima
					if info, exists, ierr := lima.Instance(cfg.Provider.Instance); ierr == nil && exists {
						vm = info.Status
					} else if ierr == nil {
						vm = "absent"
					}
				}
				fmt.Fprintf(w, "vm:        %s (%s %s) — %s\n", cfg.Provider.Instance, cfg.Provider.Type, cfg.Provider.Distro, vm)
			} else {
				fmt.Fprintf(w, "vm:        remote (unmanaged)\n")
			}

			// iceclimber summary: best-effort (a stopped/unreachable box just shows the reason).
			sctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()
			sess, err := openSession(sctx, cfg, transport)
			if err != nil {
				fmt.Fprintf(w, "iceclimber: unreachable (%v)\n", err)
				return nil
			}
			defer sess.Close()
			s := collectStatus(sctx, sess.fs, sess.runner, sess.tree)
			rt := "none"
			if len(s.Runtimes) > 0 {
				rt = strings.Join(s.Runtimes, ", ")
			}
			fmt.Fprintf(w, "iceclimber: %s · queue %d/%d awaiting · runtimes: %s\n", sess.tree.Root, s.QueueOut, s.QueueIn, rt)
			return nil
		},
	}
	return cmd
}

// ---------------------------------------------------------------------------
// shell — auto-serve jump-in (§18.2)
// ---------------------------------------------------------------------------

func newSandboxShellCmd() *cobra.Command {
	var keepServe bool
	cmd := &cobra.Command{
		Use:   "shell [name]",
		Short: "Jump into a sandbox shell with a background serve (proxy egress) for the session",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := firstNonEmpty(args, defaultSandbox())
			if id == "" {
				return fmt.Errorf("no sandbox given and no default set — name one or `sandbox create --default`")
			}
			cfg, err := config.Load(sandboxConfigPath(id), id)
			if err != nil {
				return fmt.Errorf("load sandbox %q: %w", id, err)
			}
			return autoServeShell(cmd, cfg, keepServe)
		},
	}
	cmd.Flags().BoolVar(&keepServe, "serve", false, "keep serving after the shell exits (Ctrl-C to stop) instead of tearing it down")
	return cmd
}

// autoServeShell is the jump-in: it starts a background `serve` for the session (so proxy
// egress works while you're in the box — §18.2), drops you into the interactive shell, and
// stops serve on exit. With keep=true it keeps serving after the shell exits until Ctrl-C.
// It composes the SAME serve loop (superviseServe) and the SAME interactive login the
// `serve`/`shell` commands use — no reimplementation.
func autoServeShell(cmd *cobra.Command, cfg *config.Config, keep bool) error {
	out := cmd.ErrOrStderr()

	// Take the serve lock so we don't double-serve. If another process already serves this
	// sandbox, its proxy already provides egress — skip our auto-serve and just shell in.
	lk, lerr := acquireServeLock(cfg, "sandbox shell auto-serve")
	if lerr != nil {
		fmt.Fprintf(out, "note: %v — jumping in without a new serve (egress via the existing one)\n", lerr)
		return openAndLogin(cmd, cfg)
	}
	defer lk.Release()

	serveCtx, cancel := context.WithCancel(cmd.Context())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Background serve: unattended (auto-approve), output discarded so it doesn't
		// interleave with the interactive shell. Errors (e.g. an unbootstrapped box) just
		// end the goroutine — the shell still works, only egress is absent.
		_ = superviseServe(serveCtx, cfg, transport, nil, io.Discard, false, 2*time.Second, serveHooks{})
	}()

	fmt.Fprintf(out, "auto-serve started for %s (background); egress rides the proxy\n", cfg.SandboxID)
	shellErr := openAndLogin(cmd, cfg)

	if keep {
		fmt.Fprintln(out, "shell exited — serve still running (Ctrl-C to stop)")
		waitCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		<-waitCtx.Done()
		stop()
	}
	cancel()
	wg.Wait()
	return shellErr
}

// openAndLogin opens a session and drops into the interactive login shell (the shared
// `iceclimber shell` interactive branch).
func openAndLogin(cmd *cobra.Command, cfg *config.Config) error {
	ctx := cmd.Context()
	sess, err := openSession(ctx, cfg, transport)
	if err != nil {
		return err
	}
	defer sess.Close()
	root := sess.tree.Root
	if root == "" {
		return fmt.Errorf("could not resolve the sandbox install root (ICECLIMBER_HOME)")
	}
	return interactiveLogin(ctx, sess, root)
}

// ---------------------------------------------------------------------------
// rm (§18.0)
// ---------------------------------------------------------------------------

func newSandboxRmCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "rm [name]",
		Short: "Remove a sandbox: destroy the VM (if managed) and forget the registry entry",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := firstNonEmpty(args, defaultSandbox())
			if id == "" {
				return fmt.Errorf("no sandbox given and no default set — name one to remove")
			}
			cfg, lerr := config.Load(sandboxConfigPath(id), id)
			managed := lerr == nil && cfg.Provider != nil
			w := cmd.OutOrStdout()

			isContainer := managed && cfg.Provider.Type == "container"
			if !force {
				what := "forget the registry entry (remote/unmanaged — the VM is untouched)"
				if isContainer {
					what = fmt.Sprintf("remove the container %q (the shared host VM is left running) and the registry entry", cfg.Provider.Instance)
				} else if managed {
					what = fmt.Sprintf("DESTROY the Lima VM %q and remove the registry entry", cfg.Provider.Instance)
				}
				ok, cerr := confirm(cmd, fmt.Sprintf("Remove sandbox %q? This will %s. [y/N]: ", id, what))
				if cerr != nil {
					return cerr
				}
				if !ok {
					fmt.Fprintln(w, "aborted")
					return nil
				}
			}

			// Tear down the backend ONLY when iceclimber created it (has a provider block).
			if managed {
				ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
				defer cancel()
				if isContainer {
					// Remove just the container — the shared host VM may back other sandboxes.
					if err := containerDelete(ctx, cfg.Provider); err != nil {
						return fmt.Errorf("remove container %q: %w", cfg.Provider.Instance, err)
					}
					fmt.Fprintf(w, "removed container %q (shared host VM left running)\n", cfg.Provider.Instance)
				} else if err := lima.Delete(ctx, cfg.Provider.Instance); err != nil {
					return fmt.Errorf("delete lima instance %q: %w", cfg.Provider.Instance, err)
				} else {
					fmt.Fprintf(w, "deleted lima instance %q\n", cfg.Provider.Instance)
				}
			}

			// Drop the registry dir.
			if err := os.RemoveAll(xdg.SandboxDir(id)); err != nil {
				return fmt.Errorf("remove registry dir: %w", err)
			}
			fmt.Fprintf(w, "removed sandbox %q\n", id)

			// If it was the default, reassign to the first remaining sandbox, else clear.
			if readDefaultSandbox() == id {
				remaining := listSandboxes()
				if len(remaining) > 0 {
					_ = writeDefaultSandbox(remaining[0].ID)
					fmt.Fprintf(w, "default sandbox is now %q\n", remaining[0].ID)
				} else {
					clearDefaultSandbox()
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// firstNonEmpty returns args[0] if present, else fallback.
func firstNonEmpty(args []string, fallback string) string {
	if len(args) == 1 && args[0] != "" {
		return args[0]
	}
	return fallback
}
