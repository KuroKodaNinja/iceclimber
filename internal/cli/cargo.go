package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/KuroKodaNinja/iceclimber/internal/cargo"
)

// newCargoCmd groups Cargo build-tool actions (the Rust analogue of `maven`). Today: `cargo build`.
func newCargoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cargo",
		Short: "Run the Cargo build tool in the sandbox (online through the proxy in egress-proxy mode, or air-gapped via a vendored crate relay)",
	}
	cmd.AddCommand(newCargoBuildCmd())
	return cmd
}

func newCargoBuildCmd() *cobra.Command {
	var rustVersion, project string
	cmd := &cobra.Command{
		Use:   "build [-- <cargo args>...]",
		Short: "Build a sandbox Cargo project (Cargo.toml) with cargo: online through the proxy in egress_mode: proxy; air-gapped in relay mode (controller vendors the crates, sandbox runs `cargo build --release --offline`)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" {
				return fmt.Errorf("--project <dir> (a sandbox dir with Cargo.toml) is required")
			}
			ctx, _, sess, done, err := openCommandSession(cmd, 20*time.Minute, "cargo build")
			if err != nil {
				return err
			}
			defer done()

			pr, finish := installProgress(cmd.OutOrStdout(), sess.transport)
			res, err := cargo.Build(ctx, cargoBuildDeps(sess, pr), project, rustVersion, args)
			finish()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			for _, a := range res.Artifacts {
				fmt.Fprintf(w, "built %s (%s)\n", a, res.Tier)
			}
			if len(res.Artifacts) == 0 {
				fmt.Fprintln(w, "build succeeded (no executable produced)")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "sandbox project dir with a Cargo.toml (required)")
	cmd.Flags().StringVar(&rustVersion, "rust", "", "installed sandbox Rust toolchain to build with, e.g. 1.83.0 (default: the installed toolchain)")
	return cmd
}
