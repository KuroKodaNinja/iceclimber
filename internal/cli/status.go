package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
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

			s := collectStatus(ctx, sess)
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
			fmt.Fprintf(w, "queue:     %d awaiting service, %d responses unread\n", s.QueueOut, s.QueueIn)
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
	Caps         string // "has_exec=true, has_file_write=true" or "" if not reported
}

// collectStatus reads liveness, queue depth, installed runtimes (all languages),
// and the agent's capabilities from the sandbox.
func collectStatus(ctx context.Context, sess *session) statusSnapshot {
	var s statusSnapshot
	if data, err := sess.fs.ReadFile(ctx, sess.tree.Heartbeat()); err == nil {
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
	s.QueueOut = listCount(ctx, sess, sess.tree.Outbox().New())
	s.QueueIn = listCount(ctx, sess, sess.tree.Inbox().New())
	for _, lang := range []string{"python", "node", "java"} {
		names, err := sess.fs.List(ctx, path.Join(sess.tree.Root, "runtimes", lang))
		if err != nil {
			continue
		}
		for _, n := range names {
			s.Runtimes = append(s.Runtimes, lang+" "+n)
		}
	}
	if data, err := sess.fs.ReadFile(ctx, sess.tree.Capabilities()); err == nil {
		var c struct {
			HasExec      bool `json:"has_exec"`
			HasFileWrite bool `json:"has_file_write"`
		}
		if json.Unmarshal(data, &c) == nil {
			s.Caps = fmt.Sprintf("has_exec=%v, has_file_write=%v", c.HasExec, c.HasFileWrite)
		}
	}
	return s
}

func listCount(ctx context.Context, sess *session, dir string) int {
	names, err := sess.fs.List(ctx, dir)
	if err != nil {
		return 0
	}
	return len(names)
}
