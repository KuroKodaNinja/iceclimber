package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/egress"
	"github.com/KuroKodaNinja/iceclimber/internal/java"
	"github.com/KuroKodaNinja/iceclimber/internal/maven"
	"github.com/KuroKodaNinja/iceclimber/internal/node"
	"github.com/KuroKodaNinja/iceclimber/internal/npm"
	"github.com/KuroKodaNinja/iceclimber/internal/pip"
	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/python"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
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

// PollStatus reads sandbox status over SSH and emits a StatusMsg. A failed probe
// of the install root means the sandbox is unreachable (SSH dropped) — report it so
// the panel shows an error rather than empty/stale fields.
func (o *consoleOps) PollStatus() tea.Cmd {
	return func() tea.Msg {
		if _, err := o.sess.fs.List(o.ctx, o.sess.tree.Root); err != nil {
			return tui.StatusMsg{Sandbox: o.sess.sandboxID, Err: err.Error()}
		}
		s := collectStatus(o.ctx, o.sess)
		hb := "none yet"
		if s.HeartbeatSeq != "" {
			hb = "seq " + s.HeartbeatSeq
			if s.HeartbeatAge != "" {
				hb += " · ~" + s.HeartbeatAge + " ago"
			}
		}
		return tui.StatusMsg{
			Sandbox:   o.sess.sandboxID,
			Heartbeat: hb,
			Queue:     fmt.Sprintf("%d awaiting · %d unread", s.QueueOut, s.QueueIn),
			Runtimes:  s.Runtimes,
			Caps:      s.Caps,
		}
	}
}

func (o *consoleOps) store() *egress.Store {
	if o.sess.policy == nil {
		return nil
	}
	return o.sess.policy.Store()
}

// Egress reads the operator's persisted rules + pending held requests (local files).
func (o *consoleOps) Egress() tui.EgressSnapshot {
	st := o.store()
	if st == nil {
		return tui.EgressSnapshot{}
	}
	var snap tui.EgressSnapshot
	for _, p := range st.Pending() {
		snap.Pending = append(snap.Pending, tui.EgressPending{ID: p.ID, Host: p.Host, URL: p.URL})
	}
	for _, a := range st.Allow() {
		snap.Rules = append(snap.Rules, tui.EgressRule{Kind: "allow", Pattern: a})
	}
	for _, d := range st.Deny() {
		snap.Rules = append(snap.Rules, tui.EgressRule{Kind: "deny", Pattern: d})
	}
	return snap
}

// ApprovePending resolves a held request by allowing its host and dropping it from
// pending (mirrors `iceclimber approve`); DenyPending denies the host instead.
func (o *consoleOps) ApprovePending(id string) error {
	st := o.store()
	if st == nil {
		return nil
	}
	entry, ok, err := st.RemovePending(id)
	if err != nil || !ok {
		return err
	}
	return st.AddAllow(egress.HostGlob(entry.URL))
}

func (o *consoleOps) DenyPending(id string) error {
	st := o.store()
	if st == nil {
		return nil
	}
	entry, ok, err := st.RemovePending(id)
	if err != nil || !ok {
		return err
	}
	return st.AddDeny(egress.HostGlob(entry.URL))
}

// ForgetRule removes a persisted allow/deny rule.
func (o *consoleOps) ForgetRule(kind, pattern string) error {
	st := o.store()
	if st == nil {
		return nil
	}
	if kind == "deny" {
		return st.RemoveDeny(pattern)
	}
	return st.RemoveAllow(pattern)
}

// doInstall ensures the language runtime exists (installing it at the requested
// version, or the recommended default when blank), installs the requested packages
// via the derived manager (pip / npm, tier auto), and verifies the result in the
// sandbox so Nana echoes back confirmation.
func (o *consoleOps) doInstall(r tui.InstallRequest) opResult {
	ver := defaultVersion(r.Lang, r.Version)
	specs := splitSpecs(r.Pkgs)
	switch r.Lang {
	case "python":
		echoes, err := o.ensurePython(ver)
		if err != nil {
			return opResult{typ: "python.install", err: err, echoes: echoes}
		}
		pkgs := parseSpecs(specs)
		out, err := pip.Run(o.ctx, pipDeps(o.sess), ver, pkgs, "auto")
		if err != nil {
			return opResult{typ: "pip.install", err: err, echoes: echoes}
		}
		// Verify what was *requested* (not just what was newly installed) so an
		// already-present package is still confirmed.
		echoes = append(echoes, o.verifyPyPkgs(ver, specNames(pkgs))...)
		return opResult{typ: "pip.install", detail: pkgSummary(out.Installed, out.Failed), echoes: echoes}
	case "javascript":
		echoes, err := o.ensureNode(ver)
		if err != nil {
			return opResult{typ: "node.install", err: err, echoes: echoes}
		}
		pkgs := parseNpmSpecs(specs)
		out, err := npm.Run(o.ctx, npmDeps(o.sess), ver, pkgs, "auto")
		if err != nil {
			return opResult{typ: "npm.install", err: err, echoes: echoes}
		}
		echoes = append(echoes, o.verifyNodePkgs(ver, specNames(pkgs))...)
		return opResult{typ: "npm.install", detail: pkgSummary(out.Installed, out.Failed), echoes: echoes}
	case "java":
		echoes, err := o.ensureJava(ver)
		if err != nil {
			return opResult{typ: "java.install", err: err, echoes: echoes}
		}
		coords, err := parseCoords(specs)
		if err != nil {
			return opResult{typ: "maven.install", err: err, echoes: echoes}
		}
		out, err := maven.Run(o.ctx, mavenDeps(o.sess), ver, coords, "auto")
		if err != nil {
			return opResult{typ: "maven.install", err: err, echoes: echoes}
		}
		echoes = append(echoes, mavenEchoes(out)...)
		return opResult{typ: "maven.install", detail: pkgSummary(out.Installed, out.Failed), echoes: echoes}
	}
	return opResult{typ: "install", err: fmt.Errorf("unknown language %q", r.Lang)}
}

// ensurePython locates the Python runtime at ver, installing it if absent, and
// returns a sandbox echo of the interpreter that will host the packages.
func (o *consoleOps) ensurePython(ver string) ([]echo, error) {
	bin, err := python.Locate(o.ctx, o.sess.fs, o.sess.tree.Root, ver, o.sess.fp.Arch, o.sess.fp.Libc.Family)
	if err != nil {
		res, ierr := newInstaller(o.sess).Install(o.ctx, ver)
		if ierr != nil {
			return nil, ierr
		}
		bin = res.Path
	}
	return []echo{o.verifyRuntime(bin, "-V")}, nil
}

// ensureNode locates the Node runtime at ver, installing it if absent, and returns
// a sandbox echo of the runtime that will host the packages.
func (o *consoleOps) ensureNode(ver string) ([]echo, error) {
	bin, err := node.Locate(o.ctx, o.sess.fs, o.sess.tree.Root, ver, o.sess.fp.Arch, o.sess.fp.Libc.Family)
	if err != nil {
		res, ierr := newNodeInstaller(o.sess).Install(o.ctx, ver)
		if ierr != nil {
			return nil, ierr
		}
		bin = res.Path
	}
	return []echo{o.verifyRuntime(bin, "--version")}, nil
}

// ensureJava locates the JDK at ver, installing it if absent, and returns a sandbox
// echo of the runtime that will host the resolved dependencies.
func (o *consoleOps) ensureJava(ver string) ([]echo, error) {
	bin, err := java.Locate(o.ctx, o.sess.fs, o.sess.tree.Root, ver, o.sess.fp.Arch, o.sess.fp.Libc.Family)
	if err != nil {
		res, ierr := newJavaInstaller(o.sess).Install(o.ctx, ver)
		if ierr != nil {
			return nil, ierr
		}
		bin = res.Path
	}
	return []echo{o.verifyRuntime(bin, "-version")}, nil
}

// mavenEchoes confirms the resolved coordinates and the classpath the sandbox now
// has (the resolution downloaded/relayed the JARs into the sandbox).
func mavenEchoes(out maven.Result) []echo {
	echoes := make([]echo, 0, len(out.Installed)+len(out.Failed)+1)
	for _, p := range out.Installed {
		echoes = append(echoes, echo{p.Name + ":" + p.Version + " resolved", true})
	}
	for _, f := range out.Failed {
		echoes = append(echoes, echo{f.Name + ":" + f.Version + " not resolved", false})
	}
	if out.Classpath != "" {
		echoes = append(echoes, echo{fmt.Sprintf("%d jar(s) on the classpath", strings.Count(out.Classpath, ":")+1), true})
	}
	return echoes
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

// specNames extracts the package names from parsed specs (for verification by name).
func specNames(specs []pkg.Spec) []string {
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		names = append(names, s.Name)
	}
	return names
}

// verifyPyPkgs confirms each requested package is present in the sandbox runtime via
// `pip show` (the dist name, so no import-name guessing), reading the version the
// sandbox actually has.
func (o *consoleOps) verifyPyPkgs(ver string, names []string) []echo {
	bin, err := python.Locate(o.ctx, o.sess.fs, o.sess.tree.Root, ver, o.sess.fp.Arch, o.sess.fp.Libc.Family)
	if err != nil {
		return []echo{{"python " + ver + " runtime not found to verify packages", false}}
	}
	echoes := make([]echo, 0, len(names))
	for _, name := range names {
		res, err := o.sess.runner.Run(o.ctx, remote.ShellQuote(bin)+" -m pip show "+remote.ShellQuote(name), nil)
		if err != nil || res.ExitCode != 0 {
			echoes = append(echoes, echo{name + " not present", false})
			continue
		}
		echoes = append(echoes, echo{present(name, fieldValue(res.Stdout, "Version:")), true})
	}
	return echoes
}

// verifyNodePkgs confirms each requested package's node_modules dir exists in the
// sandbox runtime (the dir name is the package name, so no require-name guessing).
func (o *consoleOps) verifyNodePkgs(ver string, names []string) []echo {
	bin, err := node.Locate(o.ctx, o.sess.fs, o.sess.tree.Root, ver, o.sess.fp.Arch, o.sess.fp.Libc.Family)
	if err != nil {
		return []echo{{"node " + ver + " runtime not found to verify packages", false}}
	}
	modules := path.Join(path.Dir(path.Dir(bin)), "lib", "node_modules")
	echoes := make([]echo, 0, len(names))
	for _, name := range names {
		data, err := o.sess.fs.ReadFile(o.ctx, path.Join(modules, name, "package.json"))
		if err != nil {
			echoes = append(echoes, echo{name + " not present in node_modules", false})
			continue
		}
		echoes = append(echoes, echo{present(name, fieldValue(data, "\"version\"")), true})
	}
	return echoes
}

// present formats a "<name> <version> present" echo (version optional).
func present(name, version string) string {
	if version == "" {
		return name + " present"
	}
	return name + " " + version + " present"
}

// fieldValue pulls a value off the first line that has the given prefix, trimming
// surrounding quotes/colons/commas — handles both `pip show` (Version: x) and
// package.json (`"version": "x"`).
func fieldValue(data []byte, prefix string) string {
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimSpace(ln)
		if rest, ok := strings.CutPrefix(ln, prefix); ok {
			return strings.Trim(strings.TrimSpace(rest), "\":, ")
		}
	}
	return ""
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
// Python 3.12, JavaScript (Node) 24 (musl arm64 needs Node ≥ 24), Java 21 (LTS).
func defaultVersion(lang, v string) string {
	if s := strings.TrimSpace(v); s != "" {
		return s
	}
	switch lang {
	case "javascript":
		return "24"
	case "java":
		return "21"
	default:
		return "3.12"
	}
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

func pkgSummary(installed []pkg.Installed, failed []pkg.Failure) string {
	parts := make([]string, 0, len(installed))
	for _, p := range installed {
		parts = append(parts, p.Name+" "+p.Version)
	}
	s := fmt.Sprintf("%d installed", len(installed))
	if len(installed) == 0 && len(failed) == 0 {
		s = "already satisfied"
	}
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
//
// Lifecycle: one session is shared by the background Serve loop and the operator
// actions (consoleOps). The dispatcher serves one request at a time and operator
// actions run as Bubble Tea cmds; both only ever read/write the sandbox over the
// SSH/SFTP transport, which is safe for concurrent use, and a single human drives
// the operator side — so they don't race in practice. On quit, the tea program
// returns, `cancel()` stops the Serve loop, and a pending approval blocked in the
// asker fails safe to deny via the done channel (ctx.Done); `sess.Close()` (deferred)
// tears down the connection.
func runConsole(parent context.Context, cfg *config.Config, transport, agentLog string) error {
	sess, err := openSession(parent, cfg, transport)
	if hke := (*remote.HostKeyError)(nil); errors.As(err, &hke) {
		// First contact with an untrusted (often ephemeral) sandbox: offer to
		// record the host key from within the console, then reconnect once.
		if tErr := trustHostInteractive(parent, cfg, hke); tErr != nil {
			return tErr
		}
		sess, err = openSession(parent, cfg, transport)
	}
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
	// If Serve dies unexpectedly (e.g. the SSH link drops) the console would look
	// alive but stop receiving events — surface that as a visible activity line.
	go func() {
		if err := disp.Serve(ctx, 2*time.Second); err != nil && !errors.Is(err, context.Canceled) {
			ev := activity.Event{
				TS: time.Now().UTC().Format(time.RFC3339), Kind: activity.KindOperated,
				Type: "serve", Status: "failed", Detail: "serving stopped: " + err.Error(),
			}
			_ = act.Append(ev)
			select {
			case events <- ev:
			default:
			}
		}
	}()

	// With no explicit --agent-log, default to the controller-side agent.log and
	// bridge the sandbox's agent stream into it, so [NANA] populates with no flag.
	logPath := agentLog
	if logPath == "" {
		logPath = agentLogPath(cfg)
		go bridgeAgentLog(ctx, sess, logPath)
	}

	ops := &consoleOps{ctx: ctx, sess: sess, act: act, events: events}
	model := tui.NewConsole(cfg.SandboxID, events, logPath, ops)
	_, err = tea.NewProgram(model, tea.WithAltScreen()).Run()
	cancel() // stop serving; any pending approval fails safe via done
	return err
}

// tailAgentSessions polls the sandbox's per-agent session.log files (written by the
// nana launcher in headless mode) and pushes new lines to the console's [NANA] pane
// — so the operator sees the agent's stream with no --agent-log flag. Each agent
// dir's log is read whole and the new suffix emitted (offset-tracked; a shrink means
// the log was rotated/truncated, so we restart). Best-effort: a missing log is just
// skipped, and a slow/full UI never blocks (non-blocking send).
func bridgeAgentLog(ctx context.Context, sess *session, dst string) {
	if dst == "" {
		return
	}
	base := path.Join(sess.tree.Root, "agent")
	offsets := map[string]int{}
	t := time.NewTicker(1500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if lines := pollAgentLogs(ctx, sess.fs, base, offsets); len(lines) > 0 {
				appendLines(dst, lines)
			}
		}
	}
}

// pollAgentLogs reads the per-agent session.log files under base and returns the
// lines that appeared since the last call, advancing offsets in place. A log shorter
// than its offset was rotated/truncated → restart from the top. Lines are prefixed
// with the agent name when more than one agent is installed. Missing logs / a missing
// base are skipped (nil).
func pollAgentLogs(ctx context.Context, fs remotefs.FS, base string, offsets map[string]int) []string {
	names, err := fs.List(ctx, base)
	if err != nil {
		return nil
	}
	multi := len(names) > 1
	var out []string
	for _, name := range names {
		logp := path.Join(base, name, "session.log")
		data, err := fs.ReadFile(ctx, logp)
		if err != nil {
			continue
		}
		off := offsets[logp]
		if len(data) < off {
			off = 0
		}
		if len(data) <= off {
			offsets[logp] = len(data)
			continue
		}
		chunk := string(data[off:])
		offsets[logp] = len(data)
		for _, line := range strings.Split(strings.TrimRight(chunk, "\n"), "\n") {
			if line == "" {
				continue
			}
			// Render stream-json tool-call events into readable lines (plain text
			// passes through); one event can yield several pane lines or none.
			for _, fl := range formatAgentLine(line) {
				if multi {
					fl = "[" + name + "] " + fl
				}
				out = append(out, fl)
			}
		}
	}
	return out
}

// appendLines appends lines to the controller-side agent-log file (best-effort).
func appendLines(dst string, lines []string) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
}

// trustHostInteractive fetches the sandbox's offered host key, shows its
// fingerprint in a modal, and (on accept) records it in known_hosts — the
// in-console equivalent of `iceclimber trust`. Declining is a hard stop: the
// security floor is never lowered silently.
func trustHostInteractive(ctx context.Context, cfg *config.Config, hke *remote.HostKeyError) error {
	key, err := remote.FetchHostKey(ctx, remote.DialConfig{
		Host: cfg.SSH.Host, Port: cfg.SSH.Port, User: cfg.SSH.User, IdentityFile: cfg.SSH.IdentityFile,
	})
	if err != nil {
		return fmt.Errorf("fetch host key for %s: %w", cfg.SandboxID, err)
	}
	info := tui.HostKeyInfo{
		SandboxID:   cfg.SandboxID,
		Address:     fmt.Sprintf("%s:%d", cfg.SSH.Host, portOr22(cfg.SSH.Port)),
		KeyType:     key.Type(),
		Fingerprint: remote.Fingerprint(key),
		Mismatch:    hke.Mismatch,
	}
	out, err := tea.NewProgram(tui.NewTrustPrompt(info), tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if err != nil {
		return err
	}
	if tp, ok := out.(tui.TrustPrompt); !ok || !tp.Accepted() {
		return fmt.Errorf("host key for %s not trusted; aborting", cfg.SandboxID)
	}
	return remote.RecordHostKey(cfg.SSH.KnownHosts, cfg.SSH.Host, cfg.SSH.Port, key, hke.Mismatch)
}
