package cli

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/node"
	"github.com/KuroKodaNinja/iceclimber/internal/npm"
	"github.com/KuroKodaNinja/iceclimber/internal/pip"
	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/python"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
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

// echo is one sandbox-side confirmation line (Nana's voice).
type echo struct {
	text string
	ok   bool
}

// opResult bundles an operator action's controller summary (typ/detail/err) with
// the sandbox-side echoes that confirm it landed.
type opResult struct {
	typ    string
	detail string
	err    error
	echoes []echo
}

func (o *consoleOps) RunInstall(r tui.InstallRequest) tea.Cmd {
	return func() tea.Msg {
		res := o.doInstall(r)
		o.record(res.typ, res.detail, res.err)
		for _, e := range res.echoes {
			o.echo(e)
		}
		return tui.OpResultMsg{}
	}
}

func (o *consoleOps) RunBootstrap() tea.Cmd {
	return func() tea.Msg {
		err := provision(o.ctx, o.sess)
		o.record("bootstrap", "tree + pip.conf + NANA.md + ping/pong smoke test", err)
		if err == nil {
			// provision's smoke test already round-tripped a ping/pong through the
			// sandbox maildir — that IS the sandbox echoing back.
			o.echo(echo{"sandbox echoed pong (ping/pong smoke test)", true})
		}
		return tui.OpResultMsg{}
	}
}

// doInstall maps the operator's language/action to the right installer and tier
// (always auto — the form doesn't expose it), applying a recommended default
// version when none was given, then verifies the result in the sandbox so Nana
// echoes back confirmation.
func (o *consoleOps) doInstall(r tui.InstallRequest) opResult {
	ver := defaultVersion(r.Lang, r.Version)
	switch r.Lang {
	case "python":
		if r.Action == "packages" {
			out, err := pip.Run(o.ctx, pipDeps(o.sess), ver, parseSpecs(splitSpecs(r.Pkgs)), "auto")
			if err != nil {
				return opResult{typ: "pip.install", err: err}
			}
			return opResult{typ: "pip.install", detail: pkgSummary(out.Installed, out.Failed),
				echoes: o.verifyPyPkgs(ver, out.Installed)}
		}
		res, err := newInstaller(o.sess).Install(o.ctx, ver)
		if err != nil {
			return opResult{typ: "python.install", err: err}
		}
		return opResult{typ: "python.install", detail: runtimeSummary("python", res.Version, res.Path, res.AlreadyInstalled),
			echoes: []echo{o.verifyRuntime(res.Path, "-V")}}
	case "javascript":
		if r.Action == "packages" {
			out, err := npm.Run(o.ctx, npmDeps(o.sess), ver, parseNpmSpecs(splitSpecs(r.Pkgs)), "auto")
			if err != nil {
				return opResult{typ: "npm.install", err: err}
			}
			return opResult{typ: "npm.install", detail: pkgSummary(out.Installed, out.Failed),
				echoes: o.verifyNodePkgs(ver, out.Installed)}
		}
		res, err := newNodeInstaller(o.sess).Install(o.ctx, ver)
		if err != nil {
			return opResult{typ: "node.install", err: err}
		}
		return opResult{typ: "node.install", detail: runtimeSummary("node", res.Version, res.Path, res.AlreadyInstalled),
			echoes: []echo{o.verifyRuntime(res.Path, "--version")}}
	}
	return opResult{typ: "install", err: fmt.Errorf("unknown language %q", r.Lang)}
}

// verifyRuntime runs the freshly-installed interpreter in the sandbox; its version
// banner is the sandbox itself confirming the runtime loads.
func (o *consoleOps) verifyRuntime(bin, flag string) echo {
	res, err := o.sess.runner.Run(o.ctx, remote.ShellQuote(bin)+" "+flag, nil)
	if err != nil || res.ExitCode != 0 {
		return echo{path.Base(bin) + " did not run in the sandbox", false}
	}
	banner := firstLine(string(res.Stdout) + string(res.Stderr))
	if banner == "" {
		banner = path.Base(bin) + " ran"
	}
	return echo{banner, true}
}

// verifyPyPkgs confirms each installed package is present in the sandbox runtime
// via `pip show` (the dist name, so no import-name guessing).
func (o *consoleOps) verifyPyPkgs(ver string, installed []pkg.Installed) []echo {
	bin, err := python.Locate(o.ctx, o.sess.fs, o.sess.tree.Root, ver, o.sess.fp.Arch, o.sess.fp.Libc.Family)
	if err != nil {
		return []echo{{"python " + ver + " runtime not found to verify packages", false}}
	}
	echoes := make([]echo, 0, len(installed))
	for _, p := range installed {
		res, err := o.sess.runner.Run(o.ctx, remote.ShellQuote(bin)+" -m pip show "+remote.ShellQuote(p.Name), nil)
		if err != nil || res.ExitCode != 0 {
			echoes = append(echoes, echo{p.Name + " not present", false})
			continue
		}
		echoes = append(echoes, echo{p.Name + " " + p.Version + " present", true})
	}
	return echoes
}

// verifyNodePkgs confirms each installed package's node_modules dir exists in the
// sandbox runtime (the dir name is the package name, so no require-name guessing).
func (o *consoleOps) verifyNodePkgs(ver string, installed []pkg.Installed) []echo {
	bin, err := node.Locate(o.ctx, o.sess.fs, o.sess.tree.Root, ver, o.sess.fp.Arch, o.sess.fp.Libc.Family)
	if err != nil {
		return []echo{{"node " + ver + " runtime not found to verify packages", false}}
	}
	modules := path.Join(path.Dir(path.Dir(bin)), "lib", "node_modules")
	echoes := make([]echo, 0, len(installed))
	for _, p := range installed {
		if _, err := o.sess.fs.ReadFile(o.ctx, path.Join(modules, p.Name, "package.json")); err != nil {
			echoes = append(echoes, echo{p.Name + " not present in node_modules", false})
			continue
		}
		echoes = append(echoes, echo{p.Name + " " + p.Version + " present", true})
	}
	return echoes
}

// firstLine returns the first non-blank line of s, trimmed.
func firstLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return ""
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

// echo records a sandbox-side confirmation (Nana's voice) — attributed to the
// sandbox so the console routes it to the [NANA] pane.
func (o *consoleOps) echo(e echo) {
	status := "ok"
	if !e.ok {
		status = "failed"
	}
	ev := activity.Event{
		TS:     time.Now().UTC().Format(time.RFC3339),
		Kind:   activity.KindVerified,
		Side:   activity.SideNana,
		Status: status,
		Detail: e.text,
	}
	_ = o.act.Append(ev)
	select {
	case o.events <- ev:
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
