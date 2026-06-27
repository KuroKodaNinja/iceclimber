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
		leaf("status", "Heartbeat age, queue depth, cache size, recent requests", "phase: serve"),
		parent("install", "Install Python or pip packages into the sandbox",
			leaf("python", "Install a portable Python runtime", "phase: python.install"),
			leaf("pip", "Install pip packages (tiered resolution)", "phase: pip.install"),
		),
		leaf("logs", "Tail the request/response/audit logs", "phase: web.fetch"),
		leaf("pending", "List controller-venue fetches held for egress approval", "phase: web.fetch (§6.1)"),
		leaf("approve", "Approve a held request (--remember to persist a rule)", "phase: web.fetch (§6.1)"),
		leaf("deny", "Deny a held request", "phase: web.fetch (§6.1)"),
		parent("cache", "Manage the local wheel/runtime cache",
			leaf("list", "List cached artifacts", "phase: pip.install"),
			leaf("prune", "Remove stale cache entries", "phase: pip.install"),
			leaf("gc", "Garbage-collect the cache", "phase: pip.install"),
		),
		parent("skill", "Manage the NANA.md skill document",
			leaf("print", "Print NANA.md to stdout", "phase: NANA.md"),
			leaf("path", "Print the NANA.md path in the sandbox", "phase: NANA.md"),
		),
		parent("nana", "Optional sandbox-side convenience binary",
			leaf("request", "Submit a request from inside the sandbox", "v2"),
			leaf("capabilities", "Self-report Nana's harness capabilities", "v2"),
		),
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
