// Package cli wires the iceclimber command surface (§9) onto cobra.
package cli

import (
	"context"

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
	root := &cobra.Command{
		Use:           "iceclimber",
		Short:         "Operate a Claude agent in an SSH-only sandbox (Popo controller / Nana sandbox)",
		SilenceUsage:  true,
		SilenceErrors: true, // main prints the error once
	}
	pf := root.PersistentFlags()
	pf.StringVar(&cfgFile, "config", "iceclimber.yaml", "path to the controller config")
	pf.StringVar(&sandboxID, "sandbox", "", "sandbox id (must match the config's sandbox_id)")
	pf.BoolVar(&jsonOutput, "json", false, "machine-readable JSON output where supported")
	pf.BoolVarP(&verbose, "verbose", "v", false, "verbose logging")

	root.AddCommand(
		newVersionCmd(),
		newInitCmd(),
		newProbeCmd(),
		newConfigCmd(),
		newBootstrapCmd(),
		newServeCmd(),
	)
	root.AddCommand(stubCommands()...)
	return root
}

// Execute runs the root command. It is the single entry point from main.
func Execute() error {
	return newRootCmd().ExecuteContext(context.Background())
}
