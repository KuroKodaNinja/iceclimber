package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/popobin"
	"github.com/KuroKodaNinja/iceclimber/internal/probe"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/runtimes"
	"github.com/KuroKodaNinja/iceclimber/internal/skill"
	"github.com/spf13/cobra"
)

func newBootstrapCmd() *cobra.Command {
	var transport string
	var force bool
	var runtimeSource string
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Create the sandbox protocol tree and run a smoke test",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()

			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()

			// Re-bootstrap is safe by default — the tree is created with mkdir -p and only
			// generated files are overwritten, so installed runtimes under <root>/runtimes
			// survive. `--force` is the opposite: a destructive reset that wipes the whole
			// sandbox tree (runtimes + state) for a clean slate, guarded against an unsafe root.
			hadRuntimes := sandboxHasRuntimes(ctx, sess)
			reset := false
			if force {
				if gErr := guardResettableRoot(sess.tree.Root); gErr != nil {
					return gErr
				}
				if rErr := sess.fs.RemoveAll(ctx, sess.tree.Root); rErr != nil {
					return fmt.Errorf("reset sandbox %s: %w", sess.tree.Root, rErr)
				}
				reset, hadRuntimes = true, false
			}

			// Detect system runtimes, resolve the per-language source (flag > config >
			// persisted > interactive prompt > managed), and persist the choice. Install
			// behavior is unchanged here — this only records intent for install/serve.
			src, err := resolveAndPersistRuntimes(cmd, cfg, sess.fp, runtimeSource)
			if err != nil {
				return err
			}

			if err := provision(ctx, sess); err != nil {
				return err
			}
			// In proxy egress mode, install the CA + per-tool trust/proxy config so the
			// sandbox's own package managers reach registries through Popo's MITM proxy.
			if err := writeEgressTrust(ctx, sess, cfg); err != nil {
				return err
			}

			state := "new sandbox"
			switch {
			case reset:
				state = "reset — removed the existing tree (runtimes + state) and reprovisioned"
			case hadRuntimes:
				state = "existing sandbox — installed runtimes/packages preserved (use --force to reset)"
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"bootstrap ok\n  sandbox:    %s\n  root:       %s\n  transport:  %s\n  state:      %s\n  smoke test: ping/pong round-trip passed\n  skill:      wrote %s\n  runtimes:   %s\n",
				cfg.SandboxID, sess.tree.Root, sess.transport, state, sess.tree.SkillFile(), src.Summary())
			fmt.Fprintf(cmd.OutOrStdout(),
				"  next:       wire NANA.md into your sandbox agent's instructions (manual — `iceclimber skill print`)\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	cmd.Flags().BoolVar(&force, "force", false, "destructive reset: wipe the sandbox tree (incl. installed runtimes) and reprovision fresh")
	cmd.Flags().StringVar(&runtimeSource, "runtime-source", "", "per-language runtime source, e.g. python=system,node=managed")
	return cmd
}

// guardResettableRoot refuses to destructively remove a root that isn't a dedicated sandbox
// directory — empty, "/", or a shallow path (< 2 segments, e.g. "/tmp" or "/home") — so a
// misconfigured remote_root can't turn `bootstrap --force` into a catastrophic rm -rf.
func guardResettableRoot(root string) error {
	r := path.Clean(root)
	if r == "" || r == "/" || r == "." {
		return fmt.Errorf("refusing to reset an unsafe remote_root %q", root)
	}
	if segs := strings.Split(strings.Trim(r, "/"), "/"); len(segs) < 2 {
		return fmt.Errorf("refusing to reset a shallow remote_root %q — point it at a dedicated sandbox dir (e.g. /tmp/iceclimber-x) to use --force", root)
	}
	return nil
}

// sandboxHasRuntimes reports whether <root>/runtimes holds any installed runtime — used to
// tell the operator a re-bootstrap preserved expensive installs (best-effort; false on error).
func sandboxHasRuntimes(ctx context.Context, sess *session) bool {
	entries, err := sess.fs.List(ctx, path.Join(sess.tree.Root, "runtimes"))
	return err == nil && len(entries) > 0
}

// resolveAndPersistRuntimes merges the runtime-source choice across layers (flag >
// config > persisted > interactive prompt > managed default), persists it
// controller-side, and returns it. The prompt fires only on an interactive terminal
// for a language with a detected system runtime and no explicit choice — so a
// headless bootstrap is unattended (defaults to managed) and never blocks.
func resolveAndPersistRuntimes(cmd *cobra.Command, cfg *config.Config, fp *probe.Fingerprint, flagStr string) (runtimes.Sources, error) {
	flagSrc, err := runtimes.ParseFlag(flagStr)
	if err != nil {
		return nil, err
	}
	store := runtimesStore(cfg)
	persisted, err := store.Load()
	if err != nil {
		return nil, err
	}
	detected := map[string]bool{}
	for _, rt := range fp.Runtimes {
		// Only offer a choice for languages whose system mode is implemented — else
		// we'd prompt for / persist a node/java=system that install can't honor.
		if runtimes.SystemSupported(rt.Lang) {
			detected[rt.Lang] = true
		}
	}

	var prompt func(string) runtimes.Source
	if isTerminal(os.Stdin) {
		reader := bufio.NewReader(os.Stdin)
		prompt = func(lang string) runtimes.Source {
			return promptRuntimeChoice(cmd.OutOrStdout(), reader, lang, fp)
		}
	}

	resolved := runtimes.Resolve(flagSrc, configRuntimeSources(cfg.Runtimes), persisted, detected, prompt)
	if err := store.Save(resolved); err != nil {
		return nil, err
	}
	return resolved, nil
}

// promptRuntimeChoice asks the operator whether to use a detected system runtime.
// Anything other than "system"/"s" keeps the managed default.
func promptRuntimeChoice(w io.Writer, r *bufio.Reader, lang string, fp *probe.Fingerprint) runtimes.Source {
	rt, _ := fp.Runtime(lang)
	fmt.Fprintf(w, "Found system %s %s at %s.\n", lang, rt.Version, rt.Path)
	fmt.Fprintf(w, "Use this system %s, or install an iceclimber-managed one? [system/managed] (default managed): ", lang)
	line, _ := r.ReadString('\n')
	switch strings.TrimSpace(strings.ToLower(line)) {
	case "system", "s":
		src := runtimes.Source{Mode: runtimes.ModeSystem}
		// Offer conda as the isolation tool when the box has it (python only).
		if lang == "python" && slices.Contains(rt.EnvManagers, "conda") {
			fmt.Fprint(w, "Isolate with venv or conda? [venv/conda] (default venv): ")
			mgr, _ := r.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(mgr)) == "conda" {
				src.EnvManager = "conda"
			}
		}
		return src
	default:
		return runtimes.Source{Mode: runtimes.ModeManaged}
	}
}

// provision runs the idempotent setup steps shared by `bootstrap` and the console's
// operator-initiated re-provision: ensure the protocol tree, write pip.conf and
// NANA.md, write a heartbeat, and run the ping/pong smoke test.
func provision(ctx context.Context, sess *session) error {
	if err := protocol.EnsureTree(ctx, sess.fs, sess.tree); err != nil {
		return err
	}
	if err := writePipConf(ctx, sess); err != nil {
		return fmt.Errorf("write pip.conf: %w", err)
	}
	if err := sess.fs.WriteFile(ctx, sess.tree.SkillFile(), []byte(skill.NanaMD)); err != nil {
		return fmt.Errorf("write NANA.md: %w", err)
	}
	if err := sess.fs.WriteFile(ctx, sess.tree.ProtocolFile(), []byte(skill.ProtocolMD)); err != nil {
		return fmt.Errorf("write PROTOCOL.md: %w", err)
	}
	// Record the host facts in the capabilities self-report so `status` shows the real
	// platform instead of "(not reported)" even before an agent is installed. Preserves
	// any existing agent block (read-modify-write). Best-effort: capabilities.json is
	// informational (Popo never depends on it), so a hiccup here must not fail the whole
	// bootstrap — matches recordAgentCaps.
	_ = protocol.WriteCapabilities(ctx, sess.fs, sess.tree, func(c *protocol.Capabilities) {
		c.Host = protocol.CapHost{OS: sess.fp.OS, Arch: sess.fp.Arch, Libc: sess.fp.Libc.Family}
	})
	if err := dropPopo(ctx, sess); err != nil {
		return err
	}
	disp := protocol.NewDispatcher(sess.fs, sess.tree, buildRegistry(sess, nil))
	if err := disp.WriteHeartbeat(ctx); err != nil {
		return err
	}
	if err := smokeTest(ctx, sess.fs, sess.tree, disp); err != nil {
		return fmt.Errorf("smoke test failed: %w", err)
	}
	return nil
}

// dropPopo relays the in-sandbox `popo` client binary to $ICECLIMBER_HOME/popo for the
// sandbox's platform. Best-effort: if no client is embedded for this platform (e.g.
// built without `make`), the agent simply falls back to the raw file protocol
// (PROTOCOL.md), so a missing client is not fatal.
func dropPopo(ctx context.Context, sess *session) error {
	bin, err := popobin.Binary(sess.fp.OS, sess.fp.Arch)
	if err != nil {
		return nil // no client for this platform; file-protocol fallback covers it
	}
	p := path.Join(sess.tree.Root, "popo")
	if err := sess.fs.WriteFile(ctx, p, bin); err != nil {
		return fmt.Errorf("write popo client: %w", err)
	}
	return sess.fs.Chmod(ctx, p, 0o755)
}

// writePipConf records the mirror in state/pip.conf so the agent's ad-hoc pip
// (via PIP_CONFIG_FILE) hits the same mirror Popo's commands use (§6.1, §3).
// No-op when no mirror is configured.
func writePipConf(ctx context.Context, sess *session) error {
	if sess.pip.IndexURL == "" {
		return nil
	}
	var b strings.Builder
	b.WriteString("[global]\n")
	fmt.Fprintf(&b, "index-url = %s\n", sess.pip.IndexURL)
	if sess.pip.ExtraIndexURL != "" {
		fmt.Fprintf(&b, "extra-index-url = %s\n", sess.pip.ExtraIndexURL)
	}
	if sess.pip.TrustedHost != "" {
		fmt.Fprintf(&b, "[install]\ntrusted-host = %s\n", sess.pip.TrustedHost)
	}
	return sess.fs.WriteFile(ctx, path.Join(sess.tree.Root, "state", "pip.conf"), []byte(b.String()))
}

// smokeTest writes a synthetic ping into the outbox, runs one dispatch cycle, and
// confirms a pong landed in the inbox — isolating "is the plumbing working" from
// "is the agent using it correctly" (plan §7 step 5).
func smokeTest(ctx context.Context, fs remotefs.FS, tree protocol.Tree, disp *protocol.Dispatcher) error {
	id := protocol.NewID()
	name := protocol.RequestName(id)
	req := protocol.Request{
		SchemaVersion: protocol.SchemaVersion,
		ID:            id,
		Type:          "ping",
		CreatedAt:     time.Now().UTC(),
		Params:        json.RawMessage("{}"),
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if err := protocol.Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
		return fmt.Errorf("deliver ping: %w", err)
	}
	if err := disp.RunOnce(ctx); err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	resp, err := protocol.ReadResponse(ctx, fs, tree, name)
	if err != nil {
		return fmt.Errorf("no pong: %w", err)
	}
	if resp.Status != protocol.StatusOK {
		return fmt.Errorf("pong status = %q", resp.Status)
	}
	// Collect our own pong (controller-side) and run one more cycle so GC prunes the
	// smoke-test pair — a fresh bootstrap shouldn't leave a permanent "1 uncollected".
	_ = protocol.AckResponse(ctx, fs, tree, name)
	_ = disp.RunOnce(ctx)
	return nil
}
