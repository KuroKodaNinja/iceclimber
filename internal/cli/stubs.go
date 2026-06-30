package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// stubCommands declares the rest of the command surface (§9) so it is
// discoverable in --help, while clearly reporting that each verb is not built
// yet and naming the build phase it belongs to (§12). They are replaced with
// real implementations phase by phase.
func stubCommands() []*cobra.Command {
	return []*cobra.Command{
		parent("cache", "Manage the local wheel/runtime cache",
			leaf("list", "List cached artifacts", "v2"),
			leaf("prune", "Remove stale cache entries", "v2"),
			leaf("gc", "Garbage-collect the cache", "v2"),
		),
		// (The old "nana" sandbox-side request stub is superseded by the real `popo`
		// client (cmd/popo, relayed to $ICECLIMBER_HOME/popo) and the `nana` launcher script.)
	}
}

func parent(use, short string, subs ...*cobra.Command) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short}
	c.AddCommand(subs...)
	return c
}

func leaf(use, short, phase string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%q is not implemented yet (%s)", cmd.Name(), phase)
		},
	}
}
