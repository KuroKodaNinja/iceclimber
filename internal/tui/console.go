package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/tail"
)

// ProgressMsg is one live install-progress sample pushed onto the console's event
// channel by the executor (with the active transport label). It drives the footer
// meter.
type ProgressMsg struct {
	progress.Event
	Transport string // "sftp" | "exec" — how the transfer is happening
}

// required is a huh validator that rejects blank/whitespace input.
func required(what string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New("enter " + what)
		}
		return nil
	}
}

// Operator choices returned on an ApprovalRequest.Reply.
const (
	Approve    = iota // allow this once
	ApproveAll        // allow + remember (per host / per verb-type)
	Deny              // deny this once
	DenyAll           // deny + remember
)

// ApprovalRequest is a held operation shown as a modal. The dispatcher (via the
// console's asker) sends it on the event channel and blocks; the operator's choice
// (one of the constants above) is sent back on Reply. The fields are
// presentation-neutral so the cli layer can fill them from its own prompt type.
type ApprovalRequest struct {
	Sandbox       string
	Title         string
	Kind          string // "operation" | "egress"
	Fields        [][2]string
	Note          string
	RememberLabel string
	Reply         chan int
}

// InstallRequest is an operator-initiated install (not an agent maildir request),
// filled from the console's install form and handed to the OpRunner. The operator
// picks a language (Python / JavaScript) and the packages to install; the cli layer
// ensures the runtime exists (installing it at Version, or the recommended default
// when blank) and derives the package manager (pip / npm) and resolution tier.
type InstallRequest struct {
	Lang    string // "python" | "javascript"
	Version string // runtime version to ensure/target (blank = recommended default)
	Pkgs    string // space/comma-separated package specs
}

// AgentInstallRequest is an operator request to install/wrap a coding agent (the
// nana wrapper). Wrap uses a binary already on the sandbox (no relay); Bin optionally
// pins its path. SkipAuth installs without configuring a token (else the token is
// read from the operator's environment).
type AgentInstallRequest struct {
	Name     string // agent name, e.g. "claude"
	Wrap     bool   // wrap an existing binary instead of relaying one in
	Bin      string // wrap only: explicit absolute path (blank = found on PATH)
	SkipAuth bool
}

// AgentChoice is one installable agent offered in the console's agent form.
type AgentChoice struct{ Name, DisplayName string }

// RuntimeChoice is a system runtime the console offers as a bootstrap source option
// (the operator can use it instead of an iceclimber-managed runtime).
type RuntimeChoice struct{ Lang, Version, Path string }

// OpResultMsg signals an operator-initiated action finished; it clears the running
// indicator. The pane line is driven separately by the activity event the runner
// emits, so the result reads identically whether it was operator- or agent-driven.
type OpResultMsg struct{}

// OpRunner executes operator-initiated actions. Each method returns a tea.Cmd that
// performs the work off the UI goroutine, appends an activity event (so the [POPO]
// pane and JSONL both reflect it), and finally emits an OpResultMsg. The cli layer
// supplies it (it holds the session); a nil OpRunner disables the management menu.
type OpRunner interface {
	RunInstall(InstallRequest) tea.Cmd
	RunBootstrap() tea.Cmd
	// RunAgentInstall installs/wraps a coding agent (the nana wrapper).
	RunAgentInstall(AgentInstallRequest) tea.Cmd
	// Agents lists the installable agents (for the agent form's picker). Local.
	Agents() []AgentChoice
	// DetectedRuntimes lists system runtimes the operator may opt into at bootstrap. Local.
	DetectedRuntimes() []RuntimeChoice
	// SetRuntimeSources persists the operator's per-language system(true)/managed(false)
	// choice from the bootstrap form. Local.
	SetRuntimeSources(useSystem map[string]bool) error
	// PollStatus returns a cmd that reads sandbox status (SSH) and emits a StatusMsg.
	PollStatus() tea.Cmd
	// Egress reads the operator's persisted rules + pending held requests (local).
	Egress() EgressSnapshot
	// ApprovePending / DenyPending resolve a held request (add a host allow/deny rule
	// and drop it from pending); ForgetRule removes a persisted rule. All local.
	ApprovePending(id string) error
	DenyPending(id string) error
	ForgetRule(kind, pattern string) error
}

// StatusSnapshot is the sandbox status shown in the console's status panel.
type StatusSnapshot struct {
	Sandbox   string
	Heartbeat string // "seq 42 · ~3s ago" or "none yet"
	Queue     string // "1 awaiting · 0 unread"
	Runtimes  []string
	Caps      string // "" if the agent hasn't reported
	Err       string // set when the sandbox is unreachable (SSH dropped); panel shows it
}

// StatusMsg delivers a fresh StatusSnapshot to the console.
type StatusMsg StatusSnapshot

// ConnState is the console's SSH connection state, driving the header indicator.
type ConnState int

const (
	ConnConnected    ConnState = iota // serving over a live connection (console default)
	ConnReconnecting                  // the link dropped; the supervisor is reconnecting
	ConnViewing                       // passive log viewer (`iceclimber tui`); not connected
)

// ConnStateMsg updates the header's connection indicator. The serve supervisor emits
// it on (re)connect and on a drop, so the header reflects reality instead of always
// claiming "serving".
type ConnStateMsg struct{ State ConnState }

// ClearServingMsg clears the in-progress "serving" indicator without a serviced event
// — used when a request is denied by the gate (which produces no serviced event and
// whose denial is logged durably, not pushed live), so the indicator never sticks.
type ClearServingMsg struct{}

// HeartbeatMsg reports a heartbeat write (the dispatcher's OnHeartbeat). The header
// uses the arrival time to show serving-vs-stale independent of the SSH link state —
// so a connected-but-wedged dispatcher (no heartbeats advancing) reads as stale.
type HeartbeatMsg struct {
	Seq int64
	At  time.Time
}

// heartbeatStale is how long without an advancing heartbeat before the header flags it
// (≈3× the 2s serve interval, with slack to avoid false positives).
const heartbeatStale = 8 * time.Second

// EgressRule is one persisted allow/deny rule; EgressPending is one held request.
type EgressRule struct{ Kind, Pattern string } // Kind: "allow" | "deny"
type EgressPending struct{ ID, Host, URL string }

// EgressSnapshot is the operator's egress state (pending first, then rules).
type EgressSnapshot struct {
	Pending []EgressPending
	Rules   []EgressRule
}

func (e EgressSnapshot) rows() int { return len(e.Pending) + len(e.Rules) }

// Console is the embed-serve operator console: it renders the live activity feed
// ([POPO]) and the agent stream ([NANA]), surfaces approval modals fed from the
// dispatcher, and (when an OpRunner is supplied) drives operator-initiated installs
// and bootstrap via huh forms. It is fed by an event channel carrying
// activity.Event (serviced requests) and *ApprovalRequest (held operations).
type Console struct {
	sandboxID string
	events    <-chan tea.Msg
	nana      *tail.Reader
	ops       OpRunner
	popoLines []popoLine
	nanaLines []string
	served    int
	approved  int
	denied    int
	lastTS    time.Time
	modal     *ApprovalRequest
	form      *huh.Form
	formKind  string // "install" | "bootstrap" while a form is open
	running   string // label of an in-flight operator action ("" = idle)
	spin      spinner.Model
	spinning  bool         // a spinner tick loop is live (so running + serving share ONE loop)
	prog      *ProgressMsg // latest progress sample for the in-flight action ("" running = ignored)
	progStart time.Time    // when the current phase began (for ETA)
	// serving is the in-flight request the dispatcher is currently servicing — distinct
	// from operator `running` so an agent-driven request and an operator action can show
	// in-flight at once. Latest-wins: a KindStarted sets it, the matching serviced/denied
	// (or a ClearServingMsg) clears it. Safe because the dispatcher services one request
	// at a time (a future dispatch-parallelism epic would need ID-keyed tracking).
	serving      string
	servingID    string
	servingStart time.Time
	panel        string // "" | "status" | "egress" — an open read/manage panel
	status       *StatusSnapshot
	egress       EgressSnapshot
	cursor       int    // selected row in the egress panel
	panelErr     string // last egress action error, shown in the panel ("" = none)
	width        int
	height       int
	connState    ConnState // SSH link state (connected vs reconnecting)
	// Heartbeat freshness — distinct from the link: a connected-but-wedged dispatcher
	// stops advancing the seq, which the header surfaces as "stale".
	lastHeartbeat time.Time
	heartbeatSeq  int64

	// form-bound values. Held behind a pointer so the huh form and every
	// (value-copied) Console share one struct — binding to &c.field directly
	// would dangle to a stale copy once Bubble Tea stores the returned model.
	st *formState
}

// formState holds the operator form's bound values.
type formState struct {
	lang    string
	version string
	pkgs    string
	confirm bool
	// agent form
	agentName string
	agentMode string // "install" | "wrap"
	agentBin  string
	agentAuth string // "env" | "skip"
	// bootstrap form: runtime source choice ("" = not offered, else "managed"|"system")
	pyRuntime string
}

// NewConsole builds a console reading events (and, optionally, the agent stream).
// ops may be nil to disable the operator management menu (e.g. in tests).
func NewConsole(sandboxID string, events <-chan tea.Msg, agentLog string, ops OpRunner) Console {
	c := Console{sandboxID: sandboxID, events: events, ops: ops, width: 100, height: 30}
	c.spin = spinner.New(spinner.WithSpinner(spinner.Dot))
	if agentLog != "" {
		c.nana = tail.NewReader(agentLog)
	}
	return c
}

// WithSeedCounts seeds the header counters from the durable activity log so they
// reflect the sandbox's real history (and survive a console restart) rather than only
// what this UI process has seen. Live events increment from here.
func (c Console) WithSeedCounts(served, approved, denied int) Console {
	c.served, c.approved, c.denied = served, approved, denied
	return c
}

func (c Console) Init() tea.Cmd { return tea.Batch(c.waitEvent(), tick()) }

func (c Console) waitEvent() tea.Cmd { return func() tea.Msg { return <-c.events } }

func (c Console) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.width, c.height = msg.Width, msg.Height
		if c.form != nil {
			c.form = c.form.WithWidth(formWidth(c.width)).WithHeight(formHeight(c.height))
		}
	case tea.KeyMsg:
		// Input precedence is intentional: an approval modal preempts everything —
		// a held operation (the dispatcher blocked, waiting) is urgent, so it must be
		// answered before a form/panel resumes. A modal can arrive over an open form
		// or panel; answering it returns control to that form/panel unchanged.
		if c.modal != nil {
			// A held operation must be answered — only y/a/n/d apply.
			if ch, ok := modalKey(msg.String()); ok {
				c.modal.Reply <- ch
				c.modal = nil
			}
			return c, nil
		}
		if c.form != nil {
			return c.updateForm(msg)
		}
		if c.panel != "" {
			return c.updatePanel(msg)
		}
		if c.running != "" {
			return c, nil // ignore input while an operator action is in flight
		}
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return c, tea.Quit
		case "i":
			if c.ops != nil {
				return c.openForm("install")
			}
		case "a":
			if c.ops != nil {
				return c.openForm("agent")
			}
		case "b":
			if c.ops != nil {
				return c.openForm("bootstrap")
			}
		case "s":
			if c.ops != nil {
				c.panel, c.status = "status", nil
				return c, c.ops.PollStatus()
			}
		case "e":
			if c.ops != nil {
				c.panel, c.cursor, c.panelErr, c.egress = "egress", 0, "", c.ops.Egress()
				return c, nil
			}
		}
	case StatusMsg:
		s := StatusSnapshot(msg)
		c.status = &s
		return c, nil
	case activity.Event:
		wasServing := c.serving != ""
		c.applyEvent(msg)
		// A request just entered service → start the spinner if it isn't already running.
		if c.serving != "" && !wasServing {
			return c, tea.Batch(c.waitEvent(), c.tickSpinner())
		}
		return c, c.waitEvent()
	case *ApprovalRequest:
		c.modal = msg
		return c, c.waitEvent()
	case ConnStateMsg:
		c.connState = msg.State
		return c, c.waitEvent()
	case ClearServingMsg:
		c.serving, c.servingID = "", ""
		return c, c.waitEvent()
	case HeartbeatMsg:
		c.lastHeartbeat, c.heartbeatSeq = msg.At, msg.Seq
		return c, c.waitEvent()
	case ProgressMsg:
		if msg.Phase != c.progPhase() {
			c.progStart = time.Now() // new phase → reset the ETA clock
		}
		m := msg
		c.prog = &m
		return c, c.waitEvent()
	case spinner.TickMsg:
		if c.running == "" && c.serving == "" {
			c.spinning = false // both idle → stop the loop (restarted when an action begins)
			return c, nil
		}
		var cmd tea.Cmd
		c.spin, cmd = c.spin.Update(msg)
		return c, cmd
	case OpResultMsg:
		c.running = ""
		c.prog = nil
		return c, nil
	case tickMsg:
		if c.nana != nil {
			for _, l := range c.nana.Poll() {
				c.addNana(l)
			}
		}
		return c, tick()
	default:
		// huh's own messages (cursor blink, etc.) while a form is open.
		if c.form != nil {
			return c.updateForm(msg)
		}
	}
	return c, nil
}

// updatePanel handles keys while a read/manage panel (status / egress) is open.
func (c Console) updatePanel(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		c.panel, c.status = "", nil
		return c, nil
	case "ctrl+c":
		return c, tea.Quit
	}
	switch c.panel {
	case "status":
		if msg.String() == "r" {
			return c, c.ops.PollStatus() // manual refresh
		}
	case "egress":
		switch msg.String() {
		case "up", "k":
			if c.cursor > 0 {
				c.cursor--
			}
		case "down", "j":
			if c.cursor < c.egress.rows()-1 {
				c.cursor++
			}
		case "a", "d":
			if p, ok := c.selectedPending(); ok {
				var err error
				if msg.String() == "a" {
					err = c.ops.ApprovePending(p.ID)
				} else {
					err = c.ops.DenyPending(p.ID)
				}
				c.panelErr = errText(err)
				c.egress = c.ops.Egress()
				c.clampCursor()
			}
		case "f":
			if r, ok := c.selectedRule(); ok {
				c.panelErr = errText(c.ops.ForgetRule(r.Kind, r.Pattern))
				c.egress = c.ops.Egress()
				c.clampCursor()
			}
		case "r":
			c.panelErr = ""
			c.egress = c.ops.Egress()
			c.clampCursor()
		}
	}
	return c, nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// selectedPending / selectedRule resolve the cursor against the combined list
// (pending entries first, then rules).
func (c Console) selectedPending() (EgressPending, bool) {
	if c.cursor >= 0 && c.cursor < len(c.egress.Pending) {
		return c.egress.Pending[c.cursor], true
	}
	return EgressPending{}, false
}

func (c Console) selectedRule() (EgressRule, bool) {
	i := c.cursor - len(c.egress.Pending)
	if i >= 0 && i < len(c.egress.Rules) {
		return c.egress.Rules[i], true
	}
	return EgressRule{}, false
}

func (c *Console) clampCursor() {
	n := c.egress.rows()
	if n == 0 {
		c.cursor = 0 // nothing to select; no row highlights (lists render empty)
		return
	}
	if c.cursor >= n {
		c.cursor = n - 1
	}
	if c.cursor < 0 {
		c.cursor = 0
	}
}

func (c Console) View() string {
	if c.modal != nil {
		return modalView(c.width, c.height, c.modal)
	}
	switch c.panel {
	case "status":
		return statusView(c.width, c.height, c.sandboxID, c.status)
	case "egress":
		return egressView(c.width, c.height, c.egress, c.cursor, c.panelErr)
	}
	if c.form != nil {
		return formView(c.width, c.height, c.formTitle(), c.form.View())
	}
	meter := ""
	if c.running != "" {
		meter = c.renderMeter()
	}
	serving := ""
	if c.serving != "" {
		serving = servingLine(c.spin.View(), c.serving, time.Since(c.servingStart))
	}
	hb := hbStatus{}
	if !c.lastHeartbeat.IsZero() {
		hb = hbStatus{known: true, seq: c.heartbeatSeq, age: time.Since(c.lastHeartbeat)}
	}
	return dashboard(c.width, c.height, c.sandboxID, c.served, c.approved, c.denied,
		c.lastTS, c.connState, hb, serving, c.popoLines, c.nanaLines, true, c.nana != nil, c.ops != nil, c.running, meter)
}

// openForm builds and focuses the named operator form.
func (c Console) openForm(kind string) (tea.Model, tea.Cmd) {
	c.formKind = kind
	switch kind {
	case "install":
		c.form = c.installForm()
	case "agent":
		c.form = c.agentForm()
	case "bootstrap":
		c.form = c.bootstrapForm()
	}
	c.form = c.form.WithWidth(formWidth(c.width)).WithHeight(formHeight(c.height)).WithShowHelp(true)
	return c, c.form.Init()
}

// updateForm delegates a message to the active form and acts on completion/abort.
func (c Console) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	f, cmd := c.form.Update(msg)
	if ff, ok := f.(*huh.Form); ok {
		c.form = ff
	}
	switch c.form.State {
	case huh.StateCompleted:
		kind := c.formKind
		c.form, c.formKind = nil, ""
		return c.submitForm(kind)
	case huh.StateAborted:
		c.form, c.formKind = nil, ""
		return c, nil
	}
	return c, cmd
}

// submitForm kicks off the operator action the completed form described.
func (c Console) submitForm(kind string) (tea.Model, tea.Cmd) {
	if c.st == nil {
		return c, nil
	}
	switch kind {
	case "install":
		c.running = installLabel(c.st.lang)
		c.progStart = time.Now()
		// Batch the spinner tick so the in-flight footer animates while the op runs.
		return c, tea.Batch(c.ops.RunInstall(InstallRequest{
			Lang: c.st.lang, Version: c.st.version, Pkgs: c.st.pkgs,
		}), c.tickSpinner())
	case "agent":
		if c.st.agentName == "" {
			return c, nil
		}
		verb := "install"
		if c.st.agentMode == "wrap" {
			verb = "wrap"
		}
		c.running = "agent " + verb
		c.progStart = time.Now()
		return c, tea.Batch(c.ops.RunAgentInstall(AgentInstallRequest{
			Name:     c.st.agentName,
			Wrap:     c.st.agentMode == "wrap",
			Bin:      c.st.agentBin,
			SkipAuth: c.st.agentAuth == "skip",
		}), c.tickSpinner())
	case "bootstrap":
		if !c.st.confirm {
			return c, nil // operator declined at the confirm
		}
		// Persist the runtime-source choice (if the form offered one) before provisioning.
		if c.st.pyRuntime != "" {
			_ = c.ops.SetRuntimeSources(map[string]bool{"python": c.st.pyRuntime == "system"})
		}
		c.running = "bootstrap"
		c.progStart = time.Now()
		return c, tea.Batch(c.ops.RunBootstrap(), c.tickSpinner())
	}
	return c, nil
}

// tickSpinner starts the spinner animation loop, but only if one isn't already live —
// so a concurrent operator action (`running`) and agent request (`serving`) share a
// single tick loop instead of double-animating (two loops would spin at 2× speed). The
// loop stops itself in the spinner.TickMsg handler once both indicators are idle.
func (c *Console) tickSpinner() tea.Cmd {
	if c.spinning {
		return nil
	}
	c.spinning = true
	return c.spin.Tick
}

// progPhase is the current progress phase, or "" when none.
func (c Console) progPhase() string {
	if c.prog == nil {
		return ""
	}
	return c.prog.Phase
}

// renderMeter builds the in-flight footer line: spinner + action label + current
// phase, with a byte bar/%/ETA (transfers) or an (i/n) count (packages), and the
// transfer mode. Falls back to spinner + label when no sample has arrived yet.
func (c Console) renderMeter() string {
	b := &strings.Builder{}
	b.WriteString(c.spin.View() + " " + c.running)
	if c.prog == nil {
		b.WriteString(" …")
		return b.String()
	}
	p := c.prog
	b.WriteString(" · " + p.Phase)
	switch {
	case p.Unit == progress.Bytes && p.Total > 0:
		ratio := float64(p.Cur) / float64(p.Total)
		fmt.Fprintf(b, "  %s %d%%  %s/%s", meterBar(ratio, 18), pct(ratio),
			progress.HumanBytes(p.Cur), progress.HumanBytes(p.Total))
		if eta := progress.ETA(p.Cur, p.Total, time.Since(c.progStart)); eta != "" {
			b.WriteString("  " + eta)
		}
	case p.Unit == progress.Items && p.Total > 0:
		fmt.Fprintf(b, "  (%d/%d)", p.Cur, p.Total)
	}
	if p.Transport != "" {
		b.WriteString(" · via " + p.Transport)
	}
	return b.String()
}

func (c *Console) installForm() *huh.Form {
	c.st = &formState{lang: "python"}
	// Pick a runtime; packages are optional. The agent — not the operator — decides
	// what to install while it writes code, so an operator install is usually just
	// the bare runtime. Naming packages here is a convenience, never a requirement.
	return huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("language").Value(&c.st.lang).Options(
			huh.NewOption("Python", "python"),
			huh.NewOption("JavaScript", "javascript"),
			huh.NewOption("Java", "java"),
		),
		huh.NewInput().Title("packages (optional)").
			Description("Blank installs just the runtime — the agent installs packages as it needs them.").
			Placeholder("e.g. requests / figlet cli-table3 / com.google.guava:guava:33.0.0-jre").
			Value(&c.st.pkgs),
		huh.NewInput().Title("version (optional)").
			Description("Blank uses the recommended default — Python 3.12 · JavaScript 24 · Java 21.").
			Placeholder("3.12 / 24 / 21").Value(&c.st.version),
	))
}

// installLabel is the friendly running indicator for an install action.
func installLabel(lang string) string {
	switch lang {
	case "javascript":
		return "JavaScript install"
	case "java":
		return "Java install"
	default:
		return "Python install"
	}
}

// agentForm collects an agent install/wrap (the nana wrapper). The auth token is read
// from the operator's environment — never typed into the form — so a secret never
// touches the UI; "skip" defers auth. The binary path applies to wrap only.
func (c *Console) agentForm() *huh.Form {
	agents := c.ops.Agents()
	opts := make([]huh.Option[string], 0, len(agents))
	for _, a := range agents {
		opts = append(opts, huh.NewOption(a.DisplayName, a.Name))
	}
	c.st = &formState{agentMode: "install", agentAuth: "env"}
	if len(agents) > 0 {
		c.st.agentName = agents[0].Name
	}
	return huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("agent").Value(&c.st.agentName).Options(opts...),
		huh.NewSelect[string]().Title("how").Value(&c.st.agentMode).Options(
			huh.NewOption("install — relay the agent binary into the sandbox", "install"),
			huh.NewOption("wrap — a binary already on the sandbox (no relay)", "wrap"),
		),
		huh.NewInput().Title("binary path (wrap only, optional)").
			Description("Absolute path on the sandbox; blank = found on PATH. Ignored for install.").
			Value(&c.st.agentBin),
		huh.NewSelect[string]().Title("auth").Value(&c.st.agentAuth).Options(
			huh.NewOption("use the token in my environment", "env"),
			huh.NewOption("skip — configure auth later", "skip"),
		),
	))
}

func (c *Console) bootstrapForm() *huh.Form {
	c.st = &formState{}
	var fields []huh.Field
	// When a system runtime is detected, let the operator pick it as the source
	// (persisted on confirm; takes effect for subsequent installs + the serve loop).
	for _, rt := range c.ops.DetectedRuntimes() {
		if rt.Lang == "python" {
			c.st.pyRuntime = "managed"
			fields = append(fields, huh.NewSelect[string]().
				Title("Python runtime").
				Description(fmt.Sprintf("A system Python %s is on the sandbox (%s).", rt.Version, rt.Path)).
				Value(&c.st.pyRuntime).Options(
				huh.NewOption("managed — install an iceclimber-pinned Python", "managed"),
				huh.NewOption("system — use the box's Python (venv under $ICECLIMBER_HOME)", "system"),
			))
		}
	}
	fields = append(fields, huh.NewConfirm().Title("Re-provision this sandbox?").
		Description("Ensure the protocol tree, pip.conf, and NANA.md, then run the ping/pong smoke test. Idempotent — existing runtimes and approvals are kept.").
		Value(&c.st.confirm))
	return huh.NewForm(huh.NewGroup(fields...))
}

func (c Console) formTitle() string {
	switch c.formKind {
	case "bootstrap":
		return "Bootstrap"
	case "agent":
		return "Install agent"
	default:
		return "Install"
	}
}

func (c *Console) applyEvent(e activity.Event) {
	if t, err := time.Parse(time.RFC3339, e.TS); err == nil {
		c.lastTS = t
	}
	// Sandbox echoes are the sandbox's own voice — show them in the [NANA] pane
	// alongside the agent's stream, not in Popo's activity.
	if e.Side == activity.SideNana {
		c.addNana(nanaEcho(e))
		return
	}
	switch e.Kind {
	case activity.KindStarted:
		// Live-only in-progress indicator: arm it (latest-wins) and stop — it's not a
		// scrollback line and not a counter. The matching serviced/denied clears it.
		c.serving, c.servingID, c.servingStart = e.Type, e.ID, time.Now()
		return
	case activity.KindServiced:
		c.served++
		c.serving, c.servingID = "", "" // completed → clear in-progress
	case activity.KindApproved:
		c.approved++
	case activity.KindDenied:
		c.denied++
		c.serving, c.servingID = "", "" // denied → clear in-progress
	}
	c.popoLines = append(c.popoLines, eventToLine(e))
	if len(c.popoLines) > maxLines {
		c.popoLines = c.popoLines[len(c.popoLines)-maxLines:]
	}
}

func (c *Console) addNana(raw string) {
	c.nanaLines = append(c.nanaLines, stripANSI(raw))
	if len(c.nanaLines) > maxLines {
		c.nanaLines = c.nanaLines[len(c.nanaLines)-maxLines:]
	}
}

func modalKey(k string) (int, bool) {
	switch k {
	case "y":
		return Approve, true
	case "a":
		return ApproveAll, true
	case "n":
		return Deny, true
	case "d":
		return DenyAll, true
	}
	return 0, false
}
