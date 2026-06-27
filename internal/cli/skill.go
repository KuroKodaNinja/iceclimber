package cli

import (
	"context"
	"fmt"
	"path"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/skill"
	"github.com/spf13/cobra"
)

func newSkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "The NANA.md sandbox skill document",
	}
	cmd.AddCommand(newSkillPrintCmd(), newSkillPathCmd())
	return cmd
}

func newSkillPrintCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "print",
		Short: "Print the NANA.md skill document (wire this into your sandbox agent)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprint(cmd.OutOrStdout(), skill.NanaMD)
			return nil
		},
	}
}

func newSkillPathCmd() *cobra.Command {
	var transport string
	cmd := &cobra.Command{
		Use:   "path",
		Short: "Print the NANA.md path inside the sandbox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			// Avoid an SSH round-trip when the root is configured.
			if cfg.RemoteRoot != "" {
				fmt.Fprintln(cmd.OutOrStdout(), path.Join(cfg.RemoteRoot, "skill", "NANA.md"))
				return nil
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()
			fmt.Fprintln(cmd.OutOrStdout(), sess.tree.SkillFile())
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	return cmd
}
