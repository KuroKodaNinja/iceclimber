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
		Short: "The sandbox skill docs (NANA.md, and PROTOCOL.md with --protocol)",
	}
	cmd.AddCommand(newSkillPrintCmd(), newSkillPathCmd())
	return cmd
}

func newSkillPrintCmd() *cobra.Command {
	var protocolDoc bool
	cmd := &cobra.Command{
		Use:   "print",
		Short: "Print NANA.md (wire it into your sandbox agent); --protocol prints PROTOCOL.md",
		Long: "Print the NANA.md contract exactly as bootstrap drops it for this sandbox — its\n" +
			"network-access section is rendered for the configured egress mode (proxy documents\n" +
			"native-tool egress through the gated proxy; relay forbids native network tools).\n" +
			"Without a loadable config it renders the default mode (proxy).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc := skill.NanaMD(printEgressMode())
			if protocolDoc {
				doc = skill.ProtocolMD
			}
			fmt.Fprint(cmd.OutOrStdout(), doc)
			return nil
		},
	}
	cmd.Flags().BoolVar(&protocolDoc, "protocol", false, "print the raw file-protocol reference (PROTOCOL.md) instead of NANA.md")
	return cmd
}

// printEgressMode resolves the egress mode for `skill print` so its NANA.md matches the one
// bootstrap dropped. Best-effort: without a loadable config (skill print is also used as
// plain documentation, outside any sandbox dir) it falls back to the default mode (proxy).
func printEgressMode() bool {
	cfg, err := config.Load(cfgFile, sandboxID)
	if err != nil {
		return true // config default: egress_mode proxy
	}
	return cfg.EgressProxy()
}

func newSkillPathCmd() *cobra.Command {
	var protocolDoc bool
	cmd := &cobra.Command{
		Use:   "path",
		Short: "Print the NANA.md path inside the sandbox; --protocol for PROTOCOL.md",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file := "NANA.md"
			if protocolDoc {
				file = "PROTOCOL.md"
			}
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			// Avoid an SSH round-trip when the root is configured.
			if cfg.RemoteRoot != "" {
				fmt.Fprintln(cmd.OutOrStdout(), path.Join(cfg.RemoteRoot, "skill", file))
				return nil
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()
			out := sess.tree.SkillFile()
			if protocolDoc {
				out = sess.tree.ProtocolFile()
			}
			fmt.Fprintln(cmd.OutOrStdout(), out)
			return nil
		},
	}
	cmd.Flags().BoolVar(&protocolDoc, "protocol", false, "print the PROTOCOL.md path instead of NANA.md")
	return cmd
}
