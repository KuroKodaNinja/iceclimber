package cli

import (
	"fmt"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and validate the controller config",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "validate",
			Short: "Load the config and report whether it is valid",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				cfg, err := config.Load(cfgFile, sandboxID)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s is valid (sandbox %q)\n", cfgFile, cfg.SandboxID)
				return nil
			},
		},
		&cobra.Command{
			Use:   "show",
			Short: "Print the parsed config (paths expanded)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				cfg, err := config.Load(cfgFile, sandboxID)
				if err != nil {
					return err
				}
				w := cmd.OutOrStdout()
				fmt.Fprintf(w, "sandbox_id:    %s\n", cfg.SandboxID)
				fmt.Fprintf(w, "ssh:           %s@%s:%d\n", cfg.SSH.User, cfg.SSH.Host, portOr22(cfg.SSH.Port))
				fmt.Fprintf(w, "identity_file: %s\n", orNone(cfg.SSH.IdentityFile))
				fmt.Fprintf(w, "remote_root:   %s\n", orNone(cfg.RemoteRoot))
				fmt.Fprintf(w, "cache_dir:     %s\n", orNone(cfg.CacheDir))
				return nil
			},
		},
	)
	return cmd
}

func orNone(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}
