package cli

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var transport string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show sandbox liveness, queue depth, runtimes, and the agent's capabilities",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()

			s := collectStatus(ctx, sess.fs, sess.runner, sess.tree)
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "sandbox:   %s  (%s, transport %s)\n", cfg.SandboxID, sess.tree.Root, sess.transport)
			if s.HeartbeatSeq == "" {
				fmt.Fprintln(w, "heartbeat: none yet — run `iceclimber serve`")
			} else {
				age := ""
				if s.HeartbeatAge != "" {
					age = fmt.Sprintf("  (~%s ago, controller clock)", s.HeartbeatAge)
				}
				fmt.Fprintf(w, "heartbeat: seq %s%s\n", s.HeartbeatSeq, age)
			}
			// "delivered" not "unread": responses stay on disk after the agent reads them
			// (the maildir isn't GC'd yet — Phase 2), so this is a historical count, not mail
			// awaiting the agent.
			fmt.Fprintf(w, "queue:     %d awaiting service · %d responses delivered\n", s.QueueOut, s.QueueIn)
			if len(s.Runtimes) == 0 {
				fmt.Fprintln(w, "runtimes:  none installed")
			} else {
				fmt.Fprintf(w, "runtimes:  %s\n", strings.Join(s.Runtimes, ", "))
			}
			if s.Caps != "" {
				fmt.Fprintf(w, "agent:     %s\n", s.Caps)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	return cmd
}

// statusSnapshot is the sandbox status gathered from the protocol tree. Shared by
// the `status` command and the console's live status panel.
type statusSnapshot struct {
	HeartbeatSeq string
	HeartbeatAge string // e.g. "3s" ("" if no parseable timestamp)
	QueueOut     int    // requests awaiting service
	QueueIn      int    // responses unread
	Runtimes     []string
	Caps         string // e.g. "Claude Code 1.2.3 · auth ✓ · linux/arm64 (glibc)", or "" if not reported
}

// collectStatus reads liveness, queue depth, installed runtimes (health-probed, all
// languages), and the agent's capabilities from the sandbox. Takes the fs/runner/tree
// directly (not *session) so it's unit-testable with fakes.
func collectStatus(ctx context.Context, fs remotefs.FS, runner remote.Runner, tree protocol.Tree) statusSnapshot {
	var s statusSnapshot
	if data, err := fs.ReadFile(ctx, tree.Heartbeat()); err == nil {
		fields := strings.Fields(strings.TrimSpace(string(data)))
		if len(fields) >= 1 {
			s.HeartbeatSeq = fields[0]
		}
		if len(fields) >= 2 {
			if t, perr := time.Parse(time.RFC3339, fields[1]); perr == nil {
				s.HeartbeatAge = time.Since(t).Round(time.Second).String()
			}
		}
	}
	s.QueueOut = listCount(ctx, fs, tree.Outbox().New())
	s.QueueIn = listCount(ctx, fs, tree.Inbox().New())
	for _, lang := range []string{"python", "node", "java"} {
		names, err := fs.List(ctx, path.Join(tree.Root, "runtimes", lang))
		if err != nil {
			continue
		}
		for _, n := range names {
			// Health-probe, not just dir presence: run the interpreter so a
			// partial/aborted extraction (or a binary that won't run on this libc) shows
			// as broken instead of "installed".
			mark := "✓"
			if !runtimeRuns(ctx, runner, tree, lang, n) {
				mark = "✗ (won't run)"
			}
			s.Runtimes = append(s.Runtimes, lang+" "+n+" "+mark)
		}
	}
	// The agent's self-report (host facts from bootstrap + the installed agent's
	// identity); absent/corrupt → "" → the panel shows "(not reported)".
	if c, err := protocol.ReadCapabilities(ctx, fs, tree); err == nil && c != nil {
		s.Caps = c.Summary()
	}
	return s
}

func listCount(ctx context.Context, fs remotefs.FS, dir string) int {
	names, err := fs.List(ctx, dir)
	if err != nil {
		return 0
	}
	return len(names)
}

// runtimeRuns reports whether the installed runtime at runtimes/<lang>/<dir> actually
// executes (its version probe exits 0) — the real "did the install succeed" signal.
func runtimeRuns(ctx context.Context, runner remote.Runner, tree protocol.Tree, lang, dir string) bool {
	exe, arg := "bin/python3", "--version"
	switch lang {
	case "node":
		exe, arg = "bin/node", "--version"
	case "java":
		exe, arg = "bin/java", "-version"
	}
	bin := path.Join(tree.Root, "runtimes", lang, dir, exe)
	res, err := runner.Run(ctx, remote.ShellQuote(bin)+" "+arg+" 2>&1", nil)
	return err == nil && res.ExitCode == 0
}
