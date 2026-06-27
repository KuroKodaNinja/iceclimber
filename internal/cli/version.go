package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// version is overridable at build time via -ldflags "-X ...cli.version=...".
var version = "0.1.0-dev"

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the iceclimber version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version)
		},
	}
}
