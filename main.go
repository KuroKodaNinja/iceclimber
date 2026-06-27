// Command iceclimber operates a Claude agent running in an SSH-only sandbox.
// One binary, two roles: Popo (controller, outside the sandbox) and Nana (the
// sandbox-side persona). See ice-climbers-plan.md for the design.
package main

import (
	"fmt"
	"os"

	"github.com/KuroKodaNinja/iceclimber/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "iceclimber:", err)
		os.Exit(1)
	}
}
