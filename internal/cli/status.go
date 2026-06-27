package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "sandbox:   %s  (%s, transport %s)\n", cfg.SandboxID, sess.tree.Root, sess.transport)
			printHeartbeat(ctx, w, sess)
			fmt.Fprintf(w, "queue:     %d awaiting service, %d responses unread\n",
				listCount(ctx, sess, sess.tree.Outbox().New()),
				listCount(ctx, sess, sess.tree.Inbox().New()))
			printRuntimes(ctx, w, sess)
			printCapabilities(ctx, w, sess)
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	return cmd
}

func listCount(ctx context.Context, sess *session, dir string) int {
	names, err := sess.fs.List(ctx, dir)
	if err != nil {
		return 0
	}
	return len(names)
}

// printHeartbeat shows Popo's liveness signal. A single status can't watch the
// seq advance, so it reports the current seq + a rough age (controller clock —
// the agent should judge liveness on seq advancement, not this timestamp).
func printHeartbeat(ctx context.Context, w io.Writer, sess *session) {
	data, err := sess.fs.ReadFile(ctx, sess.tree.Heartbeat())
	if err != nil {
		fmt.Fprintln(w, "heartbeat: none yet — run `iceclimber serve`")
		return
	}
	fields := strings.Fields(strings.TrimSpace(string(data)))
	seq, ts := "?", ""
	if len(fields) >= 1 {
		seq = fields[0]
	}
	if len(fields) >= 2 {
		ts = fields[1]
	}
	age := ""
	if t, perr := time.Parse(time.RFC3339, ts); perr == nil {
		age = fmt.Sprintf("  (~%s ago, controller clock)", time.Since(t).Round(time.Second))
	}
	fmt.Fprintf(w, "heartbeat: seq %s at %s%s\n", seq, ts, age)
}

func printRuntimes(ctx context.Context, w io.Writer, sess *session) {
	names, err := sess.fs.List(ctx, path.Join(sess.tree.Root, "runtimes", "python"))
	if err != nil || len(names) == 0 {
		fmt.Fprintln(w, "python:    none installed")
		return
	}
	fmt.Fprintf(w, "python:    %s\n", strings.Join(names, ", "))
}

func printCapabilities(ctx context.Context, w io.Writer, sess *session) {
	data, err := sess.fs.ReadFile(ctx, sess.tree.Capabilities())
	if err != nil {
		return // absent is normal — Nana hasn't reported, and Popo doesn't require it
	}
	var c struct {
		HasExec      bool `json:"has_exec"`
		HasFileWrite bool `json:"has_file_write"`
	}
	if json.Unmarshal(data, &c) != nil {
		return
	}
	fmt.Fprintf(w, "agent:     capabilities reported (has_exec=%v, has_file_write=%v)\n", c.HasExec, c.HasFileWrite)
}
