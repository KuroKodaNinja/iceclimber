package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/skill"
	"github.com/spf13/cobra"
)

func newBootstrapCmd() *cobra.Command {
	var transport string
	var force bool
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Create the sandbox protocol tree and run a smoke test",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = force // tree creation is idempotent; --force is reserved for a future destructive reset
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

			if err := provision(ctx, sess); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(),
				"bootstrap ok\n  sandbox:    %s\n  root:       %s\n  transport:  %s\n  smoke test: ping/pong round-trip passed\n  skill:      wrote %s\n",
				cfg.SandboxID, sess.tree.Root, sess.transport, sess.tree.SkillFile())
			fmt.Fprintf(cmd.OutOrStdout(),
				"  next:       wire NANA.md into your sandbox agent's instructions (manual — `iceclimber skill print`)\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	cmd.Flags().BoolVar(&force, "force", false, "re-run bootstrap (tree creation is idempotent)")
	return cmd
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
	disp := protocol.NewDispatcher(sess.fs, sess.tree, buildRegistry(sess))
	if err := disp.WriteHeartbeat(ctx); err != nil {
		return err
	}
	if err := smokeTest(ctx, sess.fs, sess.tree, disp); err != nil {
		return fmt.Errorf("smoke test failed: %w", err)
	}
	return nil
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
	return nil
}
