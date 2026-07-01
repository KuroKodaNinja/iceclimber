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
	"github.com/KuroKodaNinja/iceclimber/internal/agent"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/egress"
	"github.com/KuroKodaNinja/iceclimber/internal/java"
	"github.com/KuroKodaNinja/iceclimber/internal/maven"
	"github.com/KuroKodaNinja/iceclimber/internal/node"
	"github.com/KuroKodaNinja/iceclimber/internal/npm"
	"github.com/KuroKodaNinja/iceclimber/internal/pip"
	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/python"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/runtimes"
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
	holder *sessionHolder // current session; the supervisor swaps it on reconnect
	act    *activity.Logger
	events chan tea.Msg
}

// sess returns the current session. After an SSH drop the supervisor reconnects and
// swaps in a fresh one, so every operator action reads it live — an action attempted
// mid-drop errors against the old session and the operator simply retries.
func (o *consoleOps) sess() *session { return o.holder.Get() }

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

// Agents lists the installable agents for the console's agent picker.
func (o *consoleOps) Agents() []tui.AgentChoice {
	all := agent.All()
	out := make([]tui.AgentChoice, len(all))
	for i, d := range all {
		out[i] = tui.AgentChoice{Name: d.Name, DisplayName: d.DisplayName}
	}
	return out
}

// DetectedRuntimes lists the system runtimes the operator may opt into at bootstrap
// (those whose system mode is implemented), drawn from the probe fingerprint.
func (o *consoleOps) DetectedRuntimes() []tui.RuntimeChoice {
	var out []tui.RuntimeChoice
	for _, rt := range o.sess().fp.Runtimes {
		if runtimes.SystemSupported(rt.Lang) {
			out = append(out, tui.RuntimeChoice{Lang: rt.Lang, Version: rt.Version, Path: rt.Path, EnvManagers: rt.EnvManagers})
		}
	}
	return out
}

// SetRuntimeSources persists the operator's per-language system/managed choice. The
// install path + serve loop resolve the source fresh (runtimeSourcesNow), so the
// choice takes effect without a reconnect.
func (o *consoleOps) SetRuntimeSources(sel map[string]tui.RuntimeSelection) error {
	store := o.sess().runtimeStore
	if store == nil {
		return nil
	}
	cur, _ := store.Load()
	if cur == nil {
		cur = runtimes.Sources{}
	}
	for lang, s := range sel {
		src := runtimes.Source{Mode: runtimes.ModeManaged}
		if s.System {
			src.Mode = runtimes.ModeSystem
			src.EnvManager = s.EnvManager // "" (venv default) or "conda"; only meaningful for system
		}
		cur[lang] = src
	}
	return store.Save(cur)
}

// RunAgentInstall installs or wraps a coding agent (the nana wrapper) from the
// console — the TUI equivalent of `iceclimber agent install/wrap`.
func (o *consoleOps) RunAgentInstall(r tui.AgentInstallRequest) tea.Cmd {
	return func() tea.Msg {
		res := o.doAgentInstall(r)
		o.record(res.typ, res.detail, res.err)
		for _, e := range res.echoes {
			o.echo(e)
		}
		return tui.OpResultMsg{}
	}
}

func (o *consoleOps) doAgentInstall(r tui.AgentInstallRequest) opResult {
	verb := "agent.install"
	if r.Wrap {
		verb = "agent.wrap"
	}
	d, ok := agent.Lookup(r.Name)
	if !ok {
		return opResult{typ: verb, err: fmt.Errorf("unknown agent %q", r.Name)}
	}
	// The token is read from the operator's environment (never typed into the TUI);
	// skip-auth defers it. Mirrors the CLI's resolveAgentToken (env var, no file here).
	token := ""
	if !r.SkipAuth {
		t, err := resolveAgentToken(d, "")
		if err != nil {
			return opResult{typ: verb, err: err}
		}
		token = t
	}
	inst := newAgentInstaller(o.sess())
	var (
		res agent.Result
		err error
	)
	if r.Wrap {
		res, err = inst.Wrap(o.ctx, d, token, r.Bin)
	} else {
		res, err = inst.Install(o.ctx, d, token)
	}
	if err != nil {
		return opResult{typ: verb, err: err}
	}
	recordAgentCaps(o.ctx, o.sess(), d, res)
	detail := d.DisplayName
	if res.Version != "" {
		detail += " " + res.Version
	}
	echoes := []echo{{"agent ready: " + res.Bin, true}}
	if res.AuthConfigured {
		echoes = append(echoes, echo{"auth configured (" + d.TokenEnv + ")", true})
	} else {
		echoes = append(echoes, echo{"auth skipped — set " + d.TokenEnv + " before launching", false})
	}
	echoes = append(echoes, echo{"launch: " + res.Launcher, true})
	return opResult{typ: verb, detail: detail, echoes: echoes}
}

func (o *consoleOps) RunBootstrap() tea.Cmd {
	return func() tea.Msg {
		err := provision(o.ctx, o.sess())
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
		if _, err := o.sess().fs.List(o.ctx, o.sess().tree.Root); err != nil {
			return tui.StatusMsg{Sandbox: o.sess().sandboxID, Err: err.Error()}
		}
		sess := o.sess()
		s := collectStatus(o.ctx, sess.fs, sess.runner, sess.tree)
		hb := "none yet"
		if s.HeartbeatSeq != "" {
			hb = "seq " + s.HeartbeatSeq
			if s.HeartbeatAge != "" {
				hb += " · ~" + s.HeartbeatAge + " ago"
			}
		}
		return tui.StatusMsg{
			Sandbox:   o.sess().sandboxID,
			Heartbeat: hb,
			Queue:     fmt.Sprintf("%d awaiting service · %d awaiting collection", s.QueueOut, s.QueueIn),
			Runtimes:  s.Runtimes,
			Caps:      s.Caps,
		}
	}
}

func (o *consoleOps) store() *egress.Store {
	if o.sess().policy == nil {
		return nil
	}
	return o.sess().policy.Store()
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
// progress builds the progress.Func that pushes live install progress to the
// console as a ProgressMsg, tagged with the active transport, non-blocking (a full
// channel just drops a sample — the next one supersedes it).
func (o *consoleOps) progress() progress.Func {
	return func(e progress.Event) {
		select {
		case o.events <- tui.ProgressMsg{Event: e, Transport: o.sess().transport}:
		default:
		}
	}
}

func (o *consoleOps) doInstall(r tui.InstallRequest) opResult {
	ver := defaultVersion(r.Lang, r.Version)
	specs := splitSpecs(r.Pkgs)
	pr := o.progress()
	switch r.Lang {
	case "python":
		echoes, err := o.ensurePython(ver, pr)
		if err != nil {
			return opResult{typ: "python.install", err: err, echoes: echoes}
		}
		if len(specs) == 0 { // runtime-only: the agent installs packages as it needs them
			return opResult{typ: "python.install", detail: "python " + ver, echoes: echoes}
		}
		pkgs := parseSpecs(specs)
		out, err := pip.Run(o.ctx, pipDeps(o.sess(), pr), ver, pkgs, "auto", nil)
		if err != nil {
			return opResult{typ: "pip.install", err: err, echoes: echoes}
		}
		// Verify what was *requested* (not just what was newly installed) so an
		// already-present package is still confirmed.
		echoes = append(echoes, o.verifyPyPkgs(ver, specNames(pkgs))...)
		return opResult{typ: "pip.install", detail: pkgSummary(out.Installed, out.Failed), echoes: echoes}
	case "javascript":
		echoes, err := o.ensureNode(ver, pr)
		if err != nil {
			return opResult{typ: "node.install", err: err, echoes: echoes}
		}
		if len(specs) == 0 { // runtime-only: the agent installs packages as it needs them
			return opResult{typ: "node.install", detail: "node " + ver, echoes: echoes}
		}
		pkgs := parseNpmSpecs(specs)
		out, err := npm.Run(o.ctx, npmDeps(o.sess(), pr), ver, pkgs, "auto")
		if err != nil {
			return opResult{typ: "npm.install", err: err, echoes: echoes}
		}
		echoes = append(echoes, o.verifyNodePkgs(ver, specNames(pkgs))...)
		return opResult{typ: "npm.install", detail: pkgSummary(out.Installed, out.Failed), echoes: echoes}
	case "java":
		echoes, err := o.ensureJava(ver, pr)
		if err != nil {
			return opResult{typ: "java.install", err: err, echoes: echoes}
		}
		if len(specs) == 0 { // runtime-only: the agent resolves dependencies as it needs them
			return opResult{typ: "java.install", detail: "java " + ver, echoes: echoes}
		}
		coords, err := parseCoords(specs)
		if err != nil {
			return opResult{typ: "maven.install", err: err, echoes: echoes}
		}
		out, err := maven.Run(o.ctx, mavenDeps(o.sess(), pr), ver, coords, "auto")
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
func (o *consoleOps) ensurePython(ver string, pr progress.Func) ([]echo, error) {
	sess := o.sess()
	src := sess.runtimeSourcesNow().Of("python")
	if src.Mode == runtimes.ModeSystem {
		// System mode: create/reuse an iceclimber-owned venv from the system python.
		bin, err := python.EnsureEnv(o.ctx, sess.fs, sess.runner, sess.tree.Root, ver, sess.fp.Arch, sess.fp.Libc.Family,
			python.EnvSpec{Mode: string(src.Mode), SystemPath: sess.systemRuntimePath("python", src), EnvManager: src.EnvManager, CondaBin: sess.condaPath()})
		if err != nil {
			return nil, err
		}
		return []echo{o.verifyRuntime(bin, "-V")}, nil
	}
	bin, err := python.Locate(o.ctx, sess.fs, sess.tree.Root, ver, sess.fp.Arch, sess.fp.Libc.Family)
	if err != nil {
		res, ierr := newInstaller(sess, pr).Install(o.ctx, ver)
		if ierr != nil {
			return nil, ierr
		}
		bin = res.Path
	}
	return []echo{o.verifyRuntime(bin, "-V")}, nil
}

// ensureNode locates the Node runtime at ver, installing it if absent, and returns
// a sandbox echo of the runtime that will host the packages.
func (o *consoleOps) ensureNode(ver string, pr progress.Func) ([]echo, error) {
	bin, err := node.Locate(o.ctx, o.sess().fs, o.sess().tree.Root, ver, o.sess().fp.Arch, o.sess().fp.Libc.Family)
	if err != nil {
		res, ierr := newNodeInstaller(o.sess(), pr).Install(o.ctx, ver)
		if ierr != nil {
			return nil, ierr
		}
		bin = res.Path
	}
	return []echo{o.verifyRuntime(bin, "--version")}, nil
}

// ensureJava locates the JDK at ver, installing it if absent, and returns a sandbox
// echo of the runtime that will host the resolved dependencies.
func (o *consoleOps) ensureJava(ver string, pr progress.Func) ([]echo, error) {
	bin, err := java.Locate(o.ctx, o.sess().fs, o.sess().tree.Root, ver, o.sess().fp.Arch, o.sess().fp.Libc.Family)
	if err != nil {
		res, ierr := newJavaInstaller(o.sess(), pr).Install(o.ctx, ver)
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
	res, err := o.sess().runner.Run(o.ctx, remote.ShellQuote(bin)+" "+flag, nil)
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
	bin, err := python.Locate(o.ctx, o.sess().fs, o.sess().tree.Root, ver, o.sess().fp.Arch, o.sess().fp.Libc.Family)
	if err != nil {
		return []echo{{"python " + ver + " runtime not found to verify packages", false}}
	}
	echoes := make([]echo, 0, len(names))
	for _, name := range names {
		res, err := o.sess().runner.Run(o.ctx, remote.ShellQuote(bin)+" -m pip show "+remote.ShellQuote(name), nil)
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
	bin, err := node.Locate(o.ctx, o.sess().fs, o.sess().tree.Root, ver, o.sess().fp.Arch, o.sess().fp.Libc.Family)
	if err != nil {
		return []echo{{"node " + ver + " runtime not found to verify packages", false}}
	}
	modules := path.Join(path.Dir(path.Dir(bin)), "lib", "node_modules")
	echoes := make([]echo, 0, len(names))
	for _, name := range names {
		data, err := o.sess().fs.ReadFile(o.ctx, path.Join(modules, name, "package.json"))
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
// Lifecycle: a background supervisor owns the session, swapping it via a
// sessionHolder on each (re)connect (it closes each session when that serve cycle
// ends). The operator actions (consoleOps) and the agent.log bridge read the current
// session through the holder's mutex-guarded Get, so they follow reconnects; an
// action attempted mid-drop just errors against the old session and the operator
// retries. The dispatcher serves one request at a time and operator actions run as
// Bubble Tea cmds — both only read/write the sandbox over the SSH/SFTP transport
// (safe for concurrent use) and a single human drives the operator side, so they
// don't race in practice. On quit, the tea program returns, `cancel()` stops the
// supervisor (a pending approval blocked in the asker fails safe to deny via
// ctx.Done), and the deferred `holder.Get().Close()` backstops teardown (idempotent
// if the supervisor already closed it).
func runConsole(parent context.Context, cfg *config.Config, transport, agentLog string) error {
	// The initial connect (and host-key trust prompt) runs before the alt-screen TUI
	// so a /dev/tty password/trust prompt isn't fighting the rendered UI. A single
	// CachingPrompter is reused for reconnects so a password typed now is remembered.
	prompter := remote.NewCachingPrompter(nil)
	sess, err := openSessionWith(parent, cfg, transport, prompter)
	if hke := (*remote.HostKeyError)(nil); errors.As(err, &hke) {
		// First contact with an untrusted (often ephemeral) sandbox: offer to
		// record the host key from within the console, then reconnect once.
		if tErr := trustHostInteractive(parent, cfg, hke); tErr != nil {
			return tErr
		}
		sess, err = openSessionWith(parent, cfg, transport, prompter)
	}
	if err != nil {
		return err
	}
	prompter.Commit() // the startup dial authenticated — remember the password for reconnects

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	events := make(chan tea.Msg, 64)
	act := activity.New(activityPath(cfg))

	// The supervisor owns the session: it swaps the holder on each (re)connect and
	// closes each session when its serve cycle ends. Close the current one on return
	// as a backstop (Close is idempotent if the supervisor already closed it).
	holder := &sessionHolder{}
	holder.Set(sess)
	defer func() {
		if s := holder.Get(); s != nil {
			_ = s.Close()
		}
	}()

	emit := func(m tea.Msg) {
		select {
		case events <- m:
		default: // never stall the supervisor on a slow/closed UI
		}
	}

	// With no explicit --agent-log, default to the controller-side agent.log and
	// bridge the sandbox's agent stream into it, so [NANA] populates with no flag.
	// Reset it first (synchronously) so this session shows only its own stream — not
	// a previous run's leftover. The bridge reads the live session via the holder, so
	// it follows reconnects.
	logPath := agentLog
	if logPath == "" {
		logPath = agentLogPath(cfg)
		resetAgentLog(logPath)
		go bridgeAgentLog(ctx, holder, logPath)
	}

	// Seed the header counters from the durable activity log (authoritative, cumulative
	// per sandbox) — read NOW, before the serve goroutine appends anything, so a
	// just-serviced event isn't counted twice (seed + the live event on the channel).
	seedServed, seedApproved, seedDenied := 0, 0, 0
	if evs, rerr := activity.Read(activityPath(cfg)); rerr == nil {
		seedServed, seedApproved, seedDenied = activity.Counts(evs)
	}

	// Serve in the background with auto-reconnect. The first cycle serves the already
	// open session; on a drop the supervisor reconnects (capped backoff, forever) and
	// the header reflects the connection state.
	go func() {
		seeded := sess
		cycle := func(ctx context.Context, attempt int) (bool, error) {
			s := seeded
			seeded = nil
			if s == nil {
				var derr error
				if s, derr = openSessionWith(ctx, cfg, transport, prompter); derr != nil {
					return false, derr
				}
			}
			holder.Set(s)
			emit(tui.ConnStateMsg{State: tui.ConnConnected})
			if attempt > 0 {
				emit(activity.Event{
					TS: time.Now().UTC().Format(time.RFC3339), Kind: activity.KindOperated,
					Type: "serve", Status: "ok", Detail: "reconnected to sandbox",
				})
			}
			disp := buildConsoleDispatcher(ctx, s, cfg, act, events)
			serveErr := disp.Serve(ctx, 2*time.Second)
			_ = s.Close()
			return true, serveErr
		}
		onDown := func(err error, attempt int, backoff time.Duration) {
			emit(tui.ConnStateMsg{State: tui.ConnReconnecting})
			emit(activity.Event{
				TS: time.Now().UTC().Format(time.RFC3339), Kind: activity.KindOperated,
				Type: "serve", Status: "failed",
				Detail: fmt.Sprintf("connection lost: %v — reconnecting in %s (attempt %d)", err, backoff.Round(time.Second), attempt),
			})
		}
		_ = runSupervisor(ctx, prompter, cycle, onDown, sleepCtx)
	}()

	ops := &consoleOps{ctx: ctx, holder: holder, act: act, events: events}
	model := tui.NewConsole(cfg.SandboxID, events, logPath, ops).
		WithSeedCounts(seedServed, seedApproved, seedDenied)
	_, err = tea.NewProgram(model, tea.WithAltScreen()).Run()
	cancel() // stop serving; any pending approval fails safe via done
	return err
}

// buildConsoleDispatcher builds a dispatcher over sess for the interactive console:
// the tuiAsker approver + gate (approvals shown as modals), the registry, and the
// activity observer feeding both the JSONL and the live [POPO] event channel. Built
// fresh on every (re)connect since the dispatcher snapshots the session's fs/runner.
func buildConsoleDispatcher(ctx context.Context, sess *session, cfg *config.Config, act *activity.Logger, events chan tea.Msg) *protocol.Dispatcher {
	ap := newApprover(&tuiAsker{events: events, done: ctx.Done()}, cfg.SandboxID, act)
	sess.approver = ap
	// Agent-initiated installs report transfer progress to the console (#3), so a
	// Nana-driven transfer lights up the in-flight serving indicator. Non-blocking; the
	// transport label is this connection's (the dispatcher is rebuilt on reconnect).
	pr := func(e progress.Event) {
		select {
		case events <- tui.ProgressMsg{Event: e, Transport: sess.transport, Agent: true}:
		default:
		}
	}
	reg := buildRegistry(sess, pr)
	disp := protocol.NewDispatcher(sess.fs, sess.tree, reg)
	disp.SetRetention(cfg.Retention())
	disp.SetGate(ap.gate)
	// Surface heartbeat liveness in the header (serving vs stale), independent of the
	// SSH link state — non-blocking so the dispatcher never stalls on a slow UI.
	disp.OnHeartbeat(func(seq int64) {
		select {
		case events <- tui.HeartbeatMsg{Seq: seq, At: time.Now()}:
		default:
		}
	})
	// Surface a request the moment it's picked up (in-progress 1:1), for both agent-
	// and operator-initiated requests. Live-only: pushed to the UI, never appended to
	// the durable log — the matching serviced/denied event clears the indicator.
	disp.ObserveStart(func(ev protocol.StartEvent) {
		select {
		case events <- startedEvent(ev):
		default:
		}
	})
	disp.Observe(func(ev protocol.ServiceEvent) {
		e, ok := servicedEvent(ev)
		if !ok {
			// Denied by the gate (no serviced event, and the approver's denial is logged
			// durably but not pushed live) — still clear any in-progress indicator so it
			// never sticks. Counted as a denial via the durable log seed.
			select {
			case events <- tui.ClearServingMsg{}:
			default:
			}
			return
		}
		_ = act.Append(e)
		select {
		case events <- e:
		default: // never stall serving on a slow/closed UI
		}
	})
	return disp
}

// servicedEvent builds the activity event for a completed request, returning ok=false
// when the request was rejected by the operator gate before its handler ran — that is
// a denial, not a serviced request, so it must not inflate the serviced tally. Shared
// by the console and headless serve observers so both apply the skip identically.
func servicedEvent(ev protocol.ServiceEvent) (activity.Event, bool) {
	if isOperatorDenied(ev.Resp) {
		return activity.Event{}, false
	}
	return activity.Event{
		TS:     time.Now().UTC().Format(time.RFC3339),
		Kind:   activity.KindServiced,
		ID:     ev.Resp.ID,
		Type:   ev.Req.Type,
		Status: ev.Resp.Status,
		DurMS:  ev.Dur.Milliseconds(),
		Detail: serviceDetail(ev.Req.Type, ev.Resp),
	}, true
}

// isOperatorDenied reports whether a response was rejected by the gate before its
// handler ran (so it counts as a denial, not a serviced request).
func isOperatorDenied(resp protocol.Response) bool {
	return resp.Error != nil && resp.Error.Code == protocol.CodeOperatorDenied
}

// startedEvent builds the live in-progress event for a picked-up request. It is
// pushed to the console event channel but never appended to the durable log (a
// "right now" signal); the matching serviced/denied event clears the indicator.
func startedEvent(ev protocol.StartEvent) activity.Event {
	return activity.Event{
		TS:   time.Now().UTC().Format(time.RFC3339),
		Kind: activity.KindStarted,
		ID:   ev.Req.ID,
		Type: ev.Req.Type,
	}
}

// resetAgentLog truncates (creates empty) the controller-side agent log at the start
// of a serving session, so the [NANA] views show only the current session's agent
// stream — never a previous run's (or a test's) leftover output. Best-effort.
func resetAgentLog(path string) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, nil, 0o644)
}

// bridgeAgentLog copies new lines from the sandbox's per-agent session.log files
// (written by the nana launcher in headless mode) into the controller-side file dst,
// so every view (console / tui / logs) shows the agent's stream by tailing one local
// file — no --agent-log flag needed. The per-file read/offset/rotation logic and the
// stream-json rendering live in pollAgentLogs.
func bridgeAgentLog(ctx context.Context, holder *sessionHolder, dst string) {
	if dst == "" {
		return
	}
	offsets := map[string]int{}
	t := time.NewTicker(1500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Read the live session each tick so the bridge follows reconnects; skip
			// a tick while the link is down (between sessions).
			sess := holder.Get()
			if sess == nil {
				continue
			}
			base := path.Join(sess.tree.Root, "agent")
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
	dc := dialConfig(cfg)
	host, port, resolvedKH, err := remote.ResolveTarget(ctx, dc)
	if err != nil {
		return fmt.Errorf("resolve ssh config for %s: %w", cfg.SandboxID, err)
	}
	key, err := remote.FetchHostKey(ctx, dc)
	if err != nil {
		return fmt.Errorf("fetch host key for %s: %w", cfg.SandboxID, err)
	}
	info := tui.HostKeyInfo{
		SandboxID:   cfg.SandboxID,
		Address:     fmt.Sprintf("%s:%d", host, port),
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
	kh := cfg.SSH.KnownHosts
	if kh == "" {
		kh = resolvedKH
	}
	return remote.RecordHostKey(kh, host, port, key, hke.Mismatch)
}
