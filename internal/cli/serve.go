package cli

import (
	"context"
	"encoding/json"
	"errors"
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
			return withDispatcher(ctx, cfg, transport, deny, out, supervised, func(d *protocol.Dispatcher) error {
				fmt.Fprintf(out, "serving sandbox %s; Ctrl-C to stop\n", cfg.SandboxID)
				if supervised {
					fmt.Fprintln(out, "supervised: you'll be asked to approve each operation")
				}
				if err := d.Serve(ctx, interval); err != nil && !errors.Is(err, context.Canceled) {
					return err
				}
				return nil
			})
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

	act := activity.New(activityPath(cfg))

	// Build the approver before the registry so web.fetch's Deps receives it.
	var approver *terminalApprover
	if supervised {
		approver = newTerminalApprover(os.Stdin, out, cfg.SandboxID, act, nil)
		sess.approver = approver
	}

	reg := buildRegistry(sess)
	for _, v := range deny {
		delete(reg, v)
	}
	disp := protocol.NewDispatcher(sess.fs, sess.tree, reg)
	if approver != nil {
		// keepalive refreshes liveness right before a prompt blocks (same goroutine).
		approver.keepalive = func() { _ = disp.WriteHeartbeat(ctx) }
		disp.SetGate(approver.gate)
	}

	disp.Observe(func(ev protocol.ServiceEvent) {
		detail := serviceDetail(ev.Req.Type, ev.Resp)
		_ = act.Append(activity.Event{
			Kind:   activity.KindServiced,
			ID:     ev.Resp.ID,
			Type:   ev.Req.Type,
			Status: ev.Resp.Status,
			DurMS:  ev.Dur.Milliseconds(),
			Detail: detail,
		})
		typ := ev.Req.Type
		if typ == "" {
			typ = "?"
		}
		line := fmt.Sprintf("  %s  %-15s %-19s %s", time.Now().Format("15:04:05"), typ, ev.Resp.Status, detail)
		fmt.Fprintln(out, strings.TrimRight(line, " "))
	})
	return fn(disp)
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
