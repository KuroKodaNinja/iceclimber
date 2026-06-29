package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/tail"
	"github.com/spf13/cobra"
)

// newLogsCmd tails Popo's host-side activity log and, optionally, the sandbox
// agent's own output stream, merged into one feed tagged [POPO] / [NANA]. The
// activity log is the structured source of truth (the TUI reads the same JSONL);
// this is the plain two-tail view.
func newLogsCmd() *cobra.Command {
	var follow bool
	var agentLog string
	var tailN int
	var since time.Duration
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Tail Popo's activity (and optionally the agent's stream), merged",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			// Default to the controller-side agent.log a serving process bridges the
			// sandbox agent stream into, so [NANA] shows the agent with no flag.
			if agentLog == "" {
				agentLog = agentLogPath(cfg)
			}
			out := cmd.OutOrStdout()

			cutoff := time.Time{}
			if since > 0 {
				cutoff = time.Now().Add(-since)
			}

			// Initial dump of existing history, then continue from EOF.
			actR := tail.NewReader(activityPath(cfg))
			for _, l := range tail.LastN(filterActivity(actR.History(), cutoff), tailN) {
				if r := renderActivity(l); r != "" {
					fmt.Fprintln(out, r)
				}
			}
			var agentR *tail.Reader
			if agentLog != "" {
				agentR = tail.NewReader(agentLog)
				for _, l := range tail.LastN(agentR.History(), tailN) {
					fmt.Fprintln(out, "[NANA] "+l)
				}
			}

			if !follow {
				return nil
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
				}
				for _, l := range actR.Poll() {
					if r := renderActivity(l); r != "" {
						fmt.Fprintln(out, r)
					}
				}
				if agentR != nil {
					for _, l := range agentR.Poll() {
						fmt.Fprintln(out, "[NANA] "+l)
					}
				}
			}
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep watching for new lines")
	cmd.Flags().StringVar(&agentLog, "agent-log", "", "also tail the sandbox agent's output stream (tagged [NANA])")
	cmd.Flags().IntVarP(&tailN, "tail", "n", 0, "show only the last N lines of existing history (0 = all)")
	cmd.Flags().DurationVar(&since, "since", 0, "only show host events newer than this (e.g. 10m); applies to [POPO]")
	return cmd
}

// renderActivity turns one activity.jsonl line into a tagged [POPO] display line,
// or "" to skip an unparseable line.
func renderActivity(line string) string {
	var e activity.Event
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		return ""
	}
	return "[POPO] " + shortTime(e.TS) + "  " + eventBody(e)
}

// eventBody renders the human text of an activity event (shared by logs + tui).
func eventBody(e activity.Event) string {
	switch e.Kind {
	case activity.KindServiced:
		typ := e.Type
		if typ == "" {
			typ = "?"
		}
		return strings.TrimRight(fmt.Sprintf("%-15s %-19s %s", typ, e.Status, e.Detail), " ")
	case activity.KindApproved:
		return "approved " + e.Detail
	case activity.KindDenied:
		return "denied " + e.Detail
	default:
		return strings.TrimSpace(e.Kind + " " + e.Detail)
	}
}

func filterActivity(lines []string, cutoff time.Time) []string {
	if cutoff.IsZero() {
		return lines
	}
	out := lines[:0:0]
	for _, l := range lines {
		var e activity.Event
		if json.Unmarshal([]byte(l), &e) != nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339, e.TS); err == nil && t.Before(cutoff) {
			continue
		}
		out = append(out, l)
	}
	return out
}

func shortTime(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.Local().Format("15:04:05")
	}
	return ts
}
