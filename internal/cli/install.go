package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/pip"
	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/spf13/cobra"
)

func newInstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install Python or pip packages into the sandbox",
	}
	cmd.AddCommand(newInstallPythonCmd(), newInstallPipCmd())
	return cmd
}

func newInstallPythonCmd() *cobra.Command {
	var transport string
	cmd := &cobra.Command{
		Use:   "python <minor-version>",
		Short: "Install a portable Python runtime (python-build-standalone)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()

			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()

			res, err := newInstaller(sess).Install(ctx, args[0])
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			verb := "installed"
			if res.AlreadyInstalled {
				verb = "already installed:"
			}
			fmt.Fprintf(w, "%s python %s at %s\n", verb, res.Version, res.Path)
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	return cmd
}

func newInstallPipCmd() *cobra.Command {
	var transport, pyVersion, tier string
	cmd := &cobra.Command{
		Use:   "pip <pkg>[==version]...",
		Short: "Install pip packages into an installed runtime (Tier 0 mirror / Tier 1 relay)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()

			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()

			out, err := pip.Run(ctx, pipDeps(sess), pyVersion, parseSpecs(args), tier)
			if err != nil {
				return err
			}
			printOutcome(cmd, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	cmd.Flags().StringVar(&pyVersion, "python", "", "target python minor version, e.g. 3.12 (required)")
	cmd.Flags().StringVar(&tier, "tier", "auto", "resolution tier: auto|mirror|relay")
	_ = cmd.MarkFlagRequired("python")
	return cmd
}

// parseSpecs turns "name" / "name==version" args into package specs.
func parseSpecs(args []string) []pkg.Spec {
	specs := make([]pkg.Spec, 0, len(args))
	for _, a := range args {
		name, version, _ := strings.Cut(a, "==")
		specs = append(specs, pkg.Spec{Name: name, Version: version})
	}
	return specs
}

func printOutcome(cmd *cobra.Command, out pkg.Outcome) {
	w := cmd.OutOrStdout()
	for _, p := range out.Installed {
		fmt.Fprintf(w, "installed %s %s (%s)\n", p.Name, p.Version, p.Tier)
	}
	for _, f := range out.Failed {
		fmt.Fprintf(w, "FAILED   %s %s: %s\n", f.Name, f.Version, f.Error)
	}
	fmt.Fprintf(w, "%d installed, %d failed\n", len(out.Installed), len(out.Failed))
}
