package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	var once bool
	var transport string
	var interval time.Duration
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Watch the outbox and service requests (Popo)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}

			if once {
				ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
				defer cancel()
				return withDispatcher(ctx, cfg, transport, func(d *protocol.Dispatcher) error {
					return d.RunOnce(ctx)
				})
			}

			// Long-lived: stop cleanly on Ctrl-C / SIGTERM.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return withDispatcher(ctx, cfg, transport, func(d *protocol.Dispatcher) error {
				fmt.Fprintf(cmd.OutOrStdout(), "serving sandbox %s; Ctrl-C to stop\n", cfg.SandboxID)
				if err := d.Serve(ctx, interval); err != nil && !errors.Is(err, context.Canceled) {
					return err
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "run a single dispatch cycle and exit")
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "poll interval for the watch loop")
	return cmd
}

// withDispatcher opens a session, builds a dispatcher, runs fn, and cleans up.
func withDispatcher(ctx context.Context, cfg *config.Config, transport string, fn func(*protocol.Dispatcher) error) error {
	sess, err := openSession(ctx, cfg, transport)
	if err != nil {
		return err
	}
	defer sess.Close()
	return fn(protocol.NewDispatcher(sess.fs, sess.tree, protocol.DefaultRegistry(version)))
}
