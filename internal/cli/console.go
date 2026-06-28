package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/npm"
	"github.com/KuroKodaNinja/iceclimber/internal/pip"
	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
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

// consoleOps executes operator-initiated actions (install, bootstrap) requested
// from the console's forms. It holds the session, so each method returns a tea.Cmd
// that does the work off the UI goroutine, appends an activity event (surfacing it
// in the [POPO] pane and the JSONL just like a serviced request), and finally emits
// an OpResultMsg to clear the console's running indicator.
type consoleOps struct {
	ctx    context.Context
	sess   *session
	act    *activity.Logger
	events chan tea.Msg
}

func (o *consoleOps) RunInstall(r tui.InstallRequest) tea.Cmd {
	return func() tea.Msg {
		typ, detail, err := o.doInstall(r)
		o.record(typ, detail, err)
		return tui.OpResultMsg{}
	}
}

func (o *consoleOps) RunBootstrap() tea.Cmd {
	return func() tea.Msg {
		err := provision(o.ctx, o.sess)
		o.record("bootstrap", "tree + pip.conf + NANA.md + ping/pong smoke test", err)
		return tui.OpResultMsg{}
	}
}

// doInstall maps the operator's language/action to the right installer and tier,
// applying a recommended default version when none was given. It returns the
// activity type (the underlying verb — pip/npm/python/node), a one-line summary,
// and any error. Tier is always auto (the form doesn't expose it).
func (o *consoleOps) doInstall(r tui.InstallRequest) (string, string, error) {
	ver := defaultVersion(r.Lang, r.Version)
	switch r.Lang {
	case "python":
		if r.Action == "packages" {
			out, err := pip.Run(o.ctx, pipDeps(o.sess), ver, parseSpecs(splitSpecs(r.Pkgs)), "auto")
			if err != nil {
				return "pip.install", "", err
			}
			return "pip.install", pkgSummary(out.Installed, out.Failed), nil
		}
		res, err := newInstaller(o.sess).Install(o.ctx, ver)
		if err != nil {
			return "python.install", "", err
		}
		return "python.install", runtimeSummary("python", res.Version, res.Path, res.AlreadyInstalled), nil
	case "javascript":
		if r.Action == "packages" {
			out, err := npm.Run(o.ctx, npmDeps(o.sess), ver, parseNpmSpecs(splitSpecs(r.Pkgs)), "auto")
			if err != nil {
				return "npm.install", "", err
			}
			return "npm.install", pkgSummary(out.Installed, out.Failed), nil
		}
		res, err := newNodeInstaller(o.sess).Install(o.ctx, ver)
		if err != nil {
			return "node.install", "", err
		}
		return "node.install", runtimeSummary("node", res.Version, res.Path, res.AlreadyInstalled), nil
	}
	return "install", "", fmt.Errorf("unknown language %q", r.Lang)
}

// defaultVersion supplies a sane runtime version when the operator left it blank:
// Python 3.12, JavaScript (Node) 24 (musl arm64 needs Node ≥ 24).
func defaultVersion(lang, v string) string {
	if s := strings.TrimSpace(v); s != "" {
		return s
	}
	if lang == "javascript" {
		return "24"
	}
	return "3.12"
}

// record appends the operator action to the activity log and pushes it to the
// console (non-blocking — the UI is always receiving, so the line shows live).
func (o *consoleOps) record(typ, detail string, err error) {
	status := "ok"
	if err != nil {
		status, detail = "failed", err.Error()
	}
	e := activity.Event{
		TS:     time.Now().UTC().Format(time.RFC3339),
		Kind:   activity.KindOperated,
		Type:   typ,
		Status: status,
		Detail: detail,
	}
	_ = o.act.Append(e)
	select {
	case o.events <- e:
	default:
	}
}

func runtimeSummary(lang, version, p string, already bool) string {
	if already {
		return fmt.Sprintf("%s %s already at %s", lang, version, p)
	}
	return fmt.Sprintf("%s %s at %s", lang, version, p)
}

func pkgSummary(installed []pkg.Installed, failed []pkg.Failure) string {
	parts := make([]string, 0, len(installed))
	for _, p := range installed {
		parts = append(parts, p.Name+" "+p.Version)
	}
	s := fmt.Sprintf("%d installed", len(installed))
	if len(parts) > 0 {
		s += ": " + strings.Join(parts, ", ")
	}
	if len(failed) > 0 {
		names := make([]string, 0, len(failed))
		for _, f := range failed {
			names = append(names, f.Name)
		}
		s += fmt.Sprintf(" · %d failed: %s", len(failed), strings.Join(names, ", "))
	}
	return s
}

// splitSpecs turns a free-text "figlet, cli-table3" field into discrete args.
func splitSpecs(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' })
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

	ops := &consoleOps{ctx: ctx, sess: sess, act: act, events: events}
	model := tui.NewConsole(cfg.SandboxID, events, agentLog, ops)
	_, err = tea.NewProgram(model, tea.WithAltScreen()).Run()
	cancel() // stop serving; any pending approval fails safe via done
	return err
}
