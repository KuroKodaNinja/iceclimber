package cli

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/tui"
)

func newTuiCmd() *cobra.Command {
	var agentLog string
	var snapshot bool
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Live dashboard of Popo's activity (and, with --agent-log, the agent's stream)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			m := tui.New(cfg.SandboxID, activityPath(cfg), agentLog)
			if snapshot {
				// Render one frame and exit — non-interactive (testable, scriptable).
				fmt.Fprintln(cmd.OutOrStdout(), m.View())
				return nil
			}
			_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
			return err
		},
	}
	cmd.Flags().StringVar(&agentLog, "agent-log", "", "also show the sandbox agent's output stream ([NANA])")
	cmd.Flags().BoolVar(&snapshot, "snapshot", false, "render one frame to stdout and exit (non-interactive)")
	return cmd
}
