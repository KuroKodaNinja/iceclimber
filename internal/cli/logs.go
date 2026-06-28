package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/spf13/cobra"
)

// newLogsCmd tails Popo's host-side activity log and, optionally, the sandbox
// agent's own output stream, merged into one feed tagged [POPO] / [NANA]. The
// activity log is the structured source of truth (a future TUI reads the same
// JSONL); this is the meantime two-tail view.
func newLogsCmd() *cobra.Command {
	var follow bool
	var agentLog string
	var tail int
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
			out := cmd.OutOrStdout()
			actPath := activityPath(cfg)

			cutoff := time.Time{}
			if since > 0 {
				cutoff = time.Now().Add(-since)
			}

			// Initial dump of existing history, then continue from EOF.
			actLines, actOff := readLines(actPath)
			actLines = filterActivity(actLines, cutoff)
			actLines = lastN(actLines, tail)
			for _, l := range actLines {
				if r := renderActivity(l); r != "" {
					fmt.Fprintln(out, r)
				}
			}
			var agentOff int64
			if agentLog != "" {
				agLines, off := readLines(agentLog)
				agentOff = off
				for _, l := range lastN(agLines, tail) {
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
				var lines []string
				lines, actOff = pollFile(actPath, actOff)
				for _, l := range lines {
					if r := renderActivity(l); r != "" {
						fmt.Fprintln(out, r)
					}
				}
				if agentLog != "" {
					lines, agentOff = pollFile(agentLog, agentOff)
					for _, l := range lines {
						fmt.Fprintln(out, "[NANA] "+l)
					}
				}
			}
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep watching for new lines")
	cmd.Flags().StringVar(&agentLog, "agent-log", "", "also tail the sandbox agent's output stream (tagged [NANA])")
	cmd.Flags().IntVarP(&tail, "tail", "n", 0, "show only the last N lines of existing history (0 = all)")
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
	var body string
	switch e.Kind {
	case activity.KindServiced:
		typ := e.Type
		if typ == "" {
			typ = "?"
		}
		body = strings.TrimRight(fmt.Sprintf("%-15s %-19s %s", typ, e.Status, e.Detail), " ")
	case activity.KindApproved:
		body = "approved " + e.Detail
	case activity.KindDenied:
		body = "denied " + e.Detail
	default:
		body = strings.TrimSpace(e.Kind + " " + e.Detail)
	}
	return fmt.Sprintf("[POPO] %s  %s", shortTime(e.TS), body)
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

func lastN(lines []string, n int) []string {
	if n <= 0 || n >= len(lines) {
		return lines
	}
	return lines[len(lines)-n:]
}

// readLines reads a whole file into complete lines and returns the byte length to
// continue tailing from. Missing file = no lines, offset 0.
func readLines(path string) ([]string, int64) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0
	}
	trimmed := strings.TrimRight(string(data), "\n")
	var lines []string
	if trimmed != "" {
		lines = strings.Split(trimmed, "\n")
	}
	return lines, int64(len(data))
}

// pollFile reads complete lines appended past offset, returning them and the new
// offset. It consumes only up to the last newline (a partial trailing line waits
// for the next poll), and resets on truncation/rotation.
func pollFile(path string, offset int64) ([]string, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset // not created yet; keep waiting
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, offset
	}
	size := fi.Size()
	if size < offset {
		offset = 0 // truncated or rotated
	}
	if size == offset {
		return nil, offset
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, offset
	}
	text := string(data)
	lastNL := strings.LastIndexByte(text, '\n')
	if lastNL < 0 {
		return nil, offset // no complete line yet
	}
	consumed := text[:lastNL+1]
	lines := strings.Split(strings.TrimRight(consumed, "\n"), "\n")
	return lines, offset + int64(len(consumed))
}
