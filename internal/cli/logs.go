package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/audit"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/tail"
	"github.com/spf13/cobra"
)

// newLogsCmd tails Popo's host-side activity log, the egress audit trail, and
// (optionally) the sandbox agent's own output stream, merged into one feed tagged
// [POPO] / [EGRESS] / [NANA]. The activity + audit logs are the structured source of
// truth (the TUI reads the same JSONL); this is the plain merged-tail view.
func newLogsCmd() *cobra.Command {
	var follow bool
	var agentLog string
	var tailN int
	var since time.Duration
	var kinds []string
	var only []string
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Tail Popo's activity + egress audit (and optionally the agent's stream), merged",
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
			want := sourceFilter(only)
			kindSet := toSet(kinds)

			// Initial dump: [POPO] + [EGRESS] are both timestamped, so merge them
			// chronologically (then tail to the last N); [NANA] is an unstamped raw
			// stream, tailed on its own after.
			var actR, auditR *tail.Reader
			var merged []feedLine
			if want("popo") {
				actR = tail.NewReader(activityPath(cfg))
				for _, l := range actR.History() {
					if fl, ok := popoFeedLine(l, cutoff, kindSet); ok {
						merged = append(merged, fl)
					}
				}
			}
			if want("egress") {
				auditR = tail.NewReader(auditPath(cfg))
				for _, l := range auditR.History() {
					if fl, ok := egressFeedLine(l, cutoff); ok {
						merged = append(merged, fl)
					}
				}
			}
			sort.SliceStable(merged, func(i, j int) bool { return merged[i].t.Before(merged[j].t) })
			if tailN > 0 && len(merged) > tailN {
				merged = merged[len(merged)-tailN:]
			}
			for _, fl := range merged {
				fmt.Fprintln(out, fl.s)
			}

			var agentR *tail.Reader
			if want("nana") && agentLog != "" {
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
				if actR != nil {
					for _, l := range actR.Poll() {
						if fl, ok := popoFeedLine(l, cutoff, kindSet); ok {
							fmt.Fprintln(out, fl.s)
						}
					}
				}
				if auditR != nil {
					for _, l := range auditR.Poll() {
						if fl, ok := egressFeedLine(l, cutoff); ok {
							fmt.Fprintln(out, fl.s)
						}
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
	cmd.Flags().DurationVar(&since, "since", 0, "only show events newer than this (e.g. 10m); applies to [POPO] and [EGRESS]")
	cmd.Flags().StringSliceVar(&kinds, "kind", nil, "only show these [POPO] activity kinds (e.g. serviced,denied); repeatable/comma-separated")
	cmd.Flags().StringSliceVar(&only, "only", nil, "restrict to these sources: popo|nana|egress; repeatable/comma-separated")
	return cmd
}

// feedLine is one timestamped, already-rendered line in the merged POPO+EGRESS dump.
type feedLine struct {
	t time.Time
	s string
}

// sourceFilter returns a predicate reporting whether a source ("popo"|"nana"|"egress")
// should be shown, given the --only list (empty = all sources).
func sourceFilter(only []string) func(string) bool {
	if len(only) == 0 {
		return func(string) bool { return true }
	}
	set := toSet(only)
	return func(src string) bool { return set[src] }
}

func toSet(xs []string) map[string]bool {
	if len(xs) == 0 {
		return nil
	}
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[strings.TrimSpace(x)] = true
	}
	return m
}

// popoFeedLine parses one activity.jsonl line into a [POPO] feed line, applying the
// --since cutoff and the --kind filter (nil = all kinds). ok=false skips the line.
func popoFeedLine(line string, cutoff time.Time, kinds map[string]bool) (feedLine, bool) {
	var e activity.Event
	if json.Unmarshal([]byte(line), &e) != nil {
		return feedLine{}, false
	}
	if kinds != nil && !kinds[e.Kind] {
		return feedLine{}, false
	}
	t, _ := time.Parse(time.RFC3339, e.TS)
	if !cutoff.IsZero() && t.Before(cutoff) {
		return feedLine{}, false
	}
	return feedLine{t: t, s: "[POPO] " + shortTime(e.TS) + "  " + eventBody(e)}, true
}

// egressFeedLine parses one audit.jsonl line into an [EGRESS] feed line, applying the
// --since cutoff. ok=false skips the line.
func egressFeedLine(line string, cutoff time.Time) (feedLine, bool) {
	var e audit.Entry
	if json.Unmarshal([]byte(line), &e) != nil {
		return feedLine{}, false
	}
	t, _ := time.Parse(time.RFC3339, e.TS)
	if !cutoff.IsZero() && t.Before(cutoff) {
		return feedLine{}, false
	}
	return feedLine{t: t, s: "[EGRESS] " + shortTime(e.TS) + "  " + auditBody(e)}, true
}

// renderActivity turns one activity.jsonl line into a tagged [POPO] display line,
// or "" to skip an unparseable line. (Kept for the TUI's shared eventBody path.)
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
		base := fmt.Sprintf("%-15s %-19s %s", typ, e.Status, e.Detail)
		if e.DurMS > 0 {
			base += " · " + progress.HumanDur(time.Duration(e.DurMS)*time.Millisecond)
		}
		return strings.TrimRight(base, " ")
	case activity.KindApproved:
		return "approved " + e.Detail
	case activity.KindDenied:
		return "denied " + e.Detail
	case activity.KindStarted:
		// Live-only; not normally on disk. Render defensively for a stray line.
		typ := e.Type
		if typ == "" {
			typ = "?"
		}
		return "▸ " + typ + " …"
	default:
		return strings.TrimSpace(e.Kind + " " + e.Detail)
	}
}

// auditBody renders the human text of an egress audit entry: method + target URL, the
// venue it was served from (controller|sandbox-exec), the controller decision
// (allow|held|denied), and the outcome (status code or error). Shared by logs ([EGRESS])
// and the TUI egress panel's recent-fetch list.
func auditBody(e audit.Entry) string {
	method := e.Method
	if method == "" {
		method = "GET"
	}
	head := fmt.Sprintf("%-4s %s", method, e.URL)
	if e.Venue != "" {
		head += "  (" + e.Venue + ")"
	}
	parts := []string{head}
	if e.Decision != "" {
		parts = append(parts, e.Decision)
	}
	if e.Outcome == "error" {
		parts = append(parts, "error")
	} else if e.StatusCode > 0 {
		parts = append(parts, fmt.Sprintf("%d", e.StatusCode))
	}
	return strings.Join(parts, " · ")
}

func shortTime(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.Local().Format("15:04:05")
	}
	return ts
}
