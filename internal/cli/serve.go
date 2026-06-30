package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	var once bool
	var transport string
	var interval time.Duration
	var deny []string
	var yes bool
	var supervise bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Watch the outbox and service requests (Popo)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if once {
				ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
				defer cancel()
				// A one-shot cycle is unattended unless --supervise is explicit.
				return withDispatcher(ctx, cfg, transport, deny, out, supervise && !yes, func(d *protocol.Dispatcher) error {
					return d.RunOnce(ctx)
				})
			}

			// Supervised iff attached to a terminal or forced with --supervise (and
			// not --yes): prompt before each operation. Otherwise runs unattended.
			supervised := !yes && (supervise || isTerminal(os.Stdin))

			// Long-lived: stop cleanly on Ctrl-C / SIGTERM.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			fmt.Fprintf(out, "serving sandbox %s; Ctrl-C to stop\n", cfg.SandboxID)
			if supervised {
				fmt.Fprintln(out, "supervised: you'll be asked to approve each operation")
			}
			// Auto-reconnect on an SSH drop instead of exiting (keepalive keeps the
			// link warm; the supervisor rebuilds the session if it drops anyway).
			return superviseServe(ctx, cfg, transport, deny, out, supervised, interval, loggingServeHooks(out, cfg.SandboxID))
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "run a single dispatch cycle and exit")
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "poll interval for the watch loop")
	cmd.Flags().StringArrayVar(&deny, "deny", nil, "disable a verb, e.g. --deny web.fetch (repeatable)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "auto-approve every operation (skip the interactive prompt)")
	cmd.Flags().BoolVar(&supervise, "supervise", false, "force the approval prompt even without a terminal (reads stdin; scriptable)")
	return cmd
}

// isTerminal reports whether f is an interactive terminal (a char device).
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// runHeadless is the command-line fallback for bare `iceclimber` when there is no
// terminal for the console TUI: the same unattended serve loop `iceclimber serve`
// runs. Keeps the headless mode fully functional with a TUI present.
func runHeadless(ctx context.Context, cfg *config.Config, transport string, out io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(out, "serving sandbox %s (headless); Ctrl-C to stop\n", cfg.SandboxID)
	return superviseServe(ctx, cfg, transport, nil, out, false, 2*time.Second, loggingServeHooks(out, cfg.SandboxID))
}

// withDispatcher opens a session, builds a dispatcher (minus any denied verbs),
// wires the activity observer (durable JSONL + a live stdout feed) and — when
// supervised — the interactive approver (gate + inline egress approval), runs fn,
// and cleans up.
func withDispatcher(ctx context.Context, cfg *config.Config, transport string, deny []string, out io.Writer, supervised bool, fn func(*protocol.Dispatcher) error) error {
	sess, err := openSession(ctx, cfg, transport)
	if err != nil {
		return err
	}
	defer sess.Close()
	h := &sessionHolder{}
	h.Set(sess)
	startAgentLogBridge(ctx, cfg, h)
	disp := buildServeDispatcher(ctx, sess, cfg, deny, out, supervised)
	return fn(disp)
}

// buildServeDispatcher builds a dispatcher over an already-open session: the
// approver (when supervised) + gate, the registry minus any denied verbs, and the
// activity observer (durable JSONL + a live stdout feed). Shared by the one-shot
// path (withDispatcher) and the reconnect supervisor, which rebuilds it on every
// (re)connect since the dispatcher snapshots the session's fs/runner at
// construction. The caller owns the agent.log bridge (started once per serve run, so
// it survives reconnects without re-truncating the log).
func buildServeDispatcher(ctx context.Context, sess *session, cfg *config.Config, deny []string, out io.Writer, supervised bool) *protocol.Dispatcher {
	act := activity.New(activityPath(cfg))

	// Build the approver before the registry so web.fetch's Deps receives it.
	var ap *approver
	if supervised {
		ap = newApprover(newTerminalAsker(os.Stdin, out), cfg.SandboxID, act)
		sess.approver = ap
	}

	reg := buildRegistry(sess)
	for _, v := range deny {
		delete(reg, v)
	}
	disp := protocol.NewDispatcher(sess.fs, sess.tree, reg)
	if ap != nil {
		disp.SetGate(ap.gate)
	}

	// Mark each request as it's picked up (ephemeral — a one-line stdout receipt, no
	// JSONL append, no meter on a non-TTY pipe). The serviced line with its duration
	// follows on completion.
	disp.ObserveStart(func(ev protocol.StartEvent) {
		typ := ev.Req.Type
		if typ == "" {
			typ = "?"
		}
		fmt.Fprintf(out, "  %s  ▸ %s …\n", time.Now().Format("15:04:05"), typ)
	})
	disp.Observe(func(ev protocol.ServiceEvent) {
		e, ok := servicedEvent(ev)
		if !ok {
			return // denied by the gate — a denial, not a serviced request
		}
		_ = act.Append(e)
		typ := e.Type
		if typ == "" {
			typ = "?"
		}
		line := fmt.Sprintf("  %s  %-15s %-19s %s", time.Now().Format("15:04:05"), typ, e.Status, e.Detail)
		if e.DurMS > 0 {
			line += " · " + progress.HumanDur(time.Duration(e.DurMS)*time.Millisecond)
		}
		fmt.Fprintln(out, strings.TrimRight(line, " "))
	})

	return disp
}

// startAgentLogBridge resets the controller-side agent.log and bridges the sandbox's
// per-agent session.log(s) into it (so `iceclimber logs`/`tui` show the agent stream
// without a flag), reading the live session via holder so it follows reconnects.
// Started once per serve run.
func startAgentLogBridge(ctx context.Context, cfg *config.Config, holder *sessionHolder) {
	agentLog := agentLogPath(cfg)
	resetAgentLog(agentLog)
	go bridgeAgentLog(ctx, holder, agentLog)
}

// serviceDetail builds a short, human one-line summary of a response for the
// activity feed. It reads the result loosely (no coupling to handler structs);
// unknown shapes yield an empty detail.
func serviceDetail(reqType string, resp protocol.Response) string {
	switch resp.Status {
	case protocol.StatusNeedsClarification:
		if resp.Clarification != nil {
			return resp.Clarification.Question
		}
		return "held"
	case protocol.StatusError:
		if resp.Error != nil {
			if resp.Error.Code != "" {
				return resp.Error.Code + ": " + resp.Error.Message
			}
			return resp.Error.Message
		}
		return ""
	}
	if len(resp.Result) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(resp.Result, &m); err != nil {
		return ""
	}
	switch reqType {
	case "python.install":
		if v, _ := m["version"].(string); v != "" {
			return "python " + v
		}
	case "pip.install":
		if inst, ok := m["installed"].([]any); ok {
			parts := make([]string, 0, len(inst))
			for _, it := range inst {
				im, ok := it.(map[string]any)
				if !ok {
					continue
				}
				name, _ := im["name"].(string)
				s := name
				if ver, _ := im["version"].(string); ver != "" {
					s += " " + ver
				}
				if tier, _ := im["tier"].(string); tier != "" {
					s += " (" + tier + ")"
				}
				parts = append(parts, s)
			}
			return strings.Join(parts, ", ")
		}
	case "web.fetch":
		if sc, ok := m["status_code"].(float64); ok {
			s := strconv.Itoa(int(sc))
			if venue, _ := m["venue"].(string); venue != "" {
				s += " " + venue
			}
			return s
		}
	}
	return ""
}
