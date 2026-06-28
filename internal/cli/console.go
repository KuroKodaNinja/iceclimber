package cli

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/tui"
)

// tuiAsker presents approval prompts as console modals: it sends an
// ApprovalRequest to the console and blocks for the operator's choice. On
// shutdown (done closed) a pending approval fails safe to deny so the dispatcher
// unblocks.
type tuiAsker struct {
	events chan tea.Msg
	done   <-chan struct{}
}

func (t *tuiAsker) ask(p prompt) choice {
	reply := make(chan int, 1)
	req := &tui.ApprovalRequest{
		Sandbox: p.sandbox, Title: p.title, Kind: p.kind,
		Fields: p.fields, Note: p.note, RememberLabel: p.rememberLabel, Reply: reply,
	}
	select {
	case t.events <- req:
	case <-t.done:
		return choiceDenyOnce
	}
	select {
	case r := <-reply:
		switch r {
		case tui.ApproveAll:
			return choiceApproveRemember
		case tui.Deny:
			return choiceDenyOnce
		case tui.DenyAll:
			return choiceDenyRemember
		default: // tui.Approve
			return choiceApproveOnce
		}
	case <-t.done:
		return choiceDenyOnce
	}
}

// runConsole opens a session, runs the dispatcher in the background, and presents
// the interactive console — serving the sandbox and handling approvals inline.
// Returns when the operator quits.
func runConsole(parent context.Context, cfg *config.Config, transport, agentLog string) error {
	sess, err := openSession(parent, cfg, transport)
	if err != nil {
		return err
	}
	defer sess.Close()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	events := make(chan tea.Msg, 64)
	act := activity.New(activityPath(cfg))
	ap := newApprover(&tuiAsker{events: events, done: ctx.Done()}, cfg.SandboxID, act, nil)
	sess.approver = ap

	reg := buildRegistry(sess)
	disp := protocol.NewDispatcher(sess.fs, sess.tree, reg)
	ap.keepalive = func() { _ = disp.WriteHeartbeat(ctx) }
	disp.SetGate(ap.gate)
	disp.Observe(func(ev protocol.ServiceEvent) {
		e := activity.Event{
			TS: time.Now().UTC().Format(time.RFC3339), Kind: activity.KindServiced,
			ID: ev.Resp.ID, Type: ev.Req.Type, Status: ev.Resp.Status,
			DurMS: ev.Dur.Milliseconds(), Detail: serviceDetail(ev.Req.Type, ev.Resp),
		}
		_ = act.Append(e)
		select {
		case events <- e:
		default: // never stall serving on a slow/closed UI
		}
	})

	// Serve in the background; the console drives approvals over the event channel.
	go func() { _ = disp.Serve(ctx, 2*time.Second) }()

	model := tui.NewConsole(cfg.SandboxID, events, agentLog)
	_, err = tea.NewProgram(model, tea.WithAltScreen()).Run()
	cancel() // stop serving; any pending approval fails safe via done
	return err
}
