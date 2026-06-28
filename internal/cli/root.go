// Package cli wires the iceclimber command surface (§9) onto cobra.
package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/spf13/cobra"
)

// Persistent (global) flags, shared across commands.
var (
	cfgFile    string
	sandboxID  string
	jsonOutput bool
	verbose    bool
)

func newRootCmd() *cobra.Command {
	var consoleTransport, consoleAgentLog string
	root := &cobra.Command{
		Use:           "iceclimber",
		Short:         "Operate a Claude agent in an SSH-only sandbox (Popo controller / Nana sandbox)",
		SilenceUsage:  true,
		SilenceErrors: true, // main prints the error once
		// Bare `iceclimber` launches the interactive console (serve + watch +
		// approve). Subcommands stay for scripting/CI; `iceclimber serve` is the
		// headless path.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"no usable config (%v)\nPoint at a sandbox first: `iceclimber init` then edit %s, or pass --config.\n",
					err, cfgFile)
				return err
			}
			// The console is interactive — only launch it on a real terminal.
			// Headless (CI, pipes, no TTY) falls back to the unattended serve loop,
			// so command-line operation keeps working with the TUI present.
			if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
				fmt.Fprintln(cmd.ErrOrStderr(),
					"iceclimber: no terminal detected — running headless serve (use subcommands for other actions)")
				return runHeadless(cmd.Context(), cfg, consoleTransport, cmd.OutOrStdout())
			}
			return runConsole(cmd.Context(), cfg, consoleTransport, consoleAgentLog)
		},
	}
	pf := root.PersistentFlags()
	pf.StringVar(&cfgFile, "config", "iceclimber.yaml", "path to the controller config")
	pf.StringVar(&sandboxID, "sandbox", "", "sandbox id (must match the config's sandbox_id)")
	pf.BoolVar(&jsonOutput, "json", false, "machine-readable JSON output where supported")
	pf.BoolVarP(&verbose, "verbose", "v", false, "verbose logging")
	root.Flags().StringVar(&consoleTransport, "transport", "auto", "remote FS transport for the console: auto|sftp|exec")
	root.Flags().StringVar(&consoleAgentLog, "agent-log", "", "also show the sandbox agent's output stream ([NANA])")

	root.AddCommand(
		newVersionCmd(),
		newInitCmd(),
		newProbeCmd(),
		newConfigCmd(),
		newBootstrapCmd(),
		newServeCmd(),
		newInstallCmd(),
		newWebCmd(),
		newPendingCmd(),
		newApproveCmd(),
		newDenyCmd(),
		newSkillCmd(),
		newStatusCmd(),
		newLogsCmd(),
		newTuiCmd(),
	)
	root.AddCommand(stubCommands()...)
	return root
}

// Execute runs the root command. It is the single entry point from main.
func Execute() error {
	return newRootCmd().ExecuteContext(context.Background())
}
