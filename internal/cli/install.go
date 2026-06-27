package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/spf13/cobra"
)

func newInstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install Python or pip packages into the sandbox",
	}
	cmd.AddCommand(newInstallPythonCmd(), newInstallPipStub())
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

func newInstallPipStub() *cobra.Command {
	return &cobra.Command{
		Use:   "pip",
		Short: "Install pip packages (tiered resolution)",
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("%q is not implemented yet (phase: pip.install)", "pip")
		},
	}
}
