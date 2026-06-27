package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
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

			if err := protocol.EnsureTree(ctx, sess.fs, sess.tree); err != nil {
				return err
			}
			disp := protocol.NewDispatcher(sess.fs, sess.tree, protocol.DefaultRegistry(version))
			if err := disp.WriteHeartbeat(ctx); err != nil {
				return err
			}
			if err := smokeTest(ctx, sess.fs, sess.tree, disp); err != nil {
				return fmt.Errorf("smoke test failed: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(),
				"bootstrap ok\n  sandbox:    %s\n  root:       %s\n  transport:  %s\n  smoke test: ping/pong round-trip passed\n",
				cfg.SandboxID, sess.tree.Root, sess.transport)
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	cmd.Flags().BoolVar(&force, "force", false, "re-run bootstrap (tree creation is idempotent)")
	return cmd
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
