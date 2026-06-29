package tui

import (
	"errors"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/tail"
)

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

// OpResultMsg signals an operator-initiated action finished; it clears the running
// indicator. The pane line is driven separately by the activity event the runner
// emits, so the result reads identically whether it was operator- or agent-driven.
type OpResultMsg struct{}

// AgentLine is one line of an agent's own output stream — from a sandbox session.log
// the console auto-tails (no --agent-log needed) or the --agent-log file. It renders
// in the [NANA] pane, the sandbox's voice.
type AgentLine struct{ Text string }

// OpRunner executes operator-initiated actions. Each method returns a tea.Cmd that
// performs the work off the UI goroutine, appends an activity event (so the [POPO]
// pane and JSONL both reflect it), and finally emits an OpResultMsg. The cli layer
// supplies it (it holds the session); a nil OpRunner disables the management menu.
type OpRunner interface {
	RunInstall(InstallRequest) tea.Cmd
	RunBootstrap() tea.Cmd
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
	panel     string // "" | "status" | "egress" — an open read/manage panel
	status    *StatusSnapshot
	egress    EgressSnapshot
	cursor    int    // selected row in the egress panel
	panelErr  string // last egress action error, shown in the panel ("" = none)
	width     int
	height    int

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
}

// NewConsole builds a console reading events (and, optionally, the agent stream).
// ops may be nil to disable the operator management menu (e.g. in tests).
func NewConsole(sandboxID string, events <-chan tea.Msg, agentLog string, ops OpRunner) Console {
	c := Console{sandboxID: sandboxID, events: events, ops: ops, width: 100, height: 30}
	if agentLog != "" {
		c.nana = tail.NewReader(agentLog)
	}
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
		c.applyEvent(msg)
		return c, c.waitEvent()
	case *ApprovalRequest:
		c.modal = msg
		return c, c.waitEvent()
	case OpResultMsg:
		c.running = ""
		return c, nil
	case AgentLine:
		c.addNana(msg.Text)
		return c, c.waitEvent()
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
	return dashboard(c.width, c.height, c.sandboxID, c.served, c.approved, c.denied,
		c.lastTS, true, c.popoLines, c.nanaLines, true, c.nana != nil, c.ops != nil, c.running)
}

// openForm builds and focuses the named operator form.
func (c Console) openForm(kind string) (tea.Model, tea.Cmd) {
	c.formKind = kind
	switch kind {
	case "install":
		c.form = c.installForm()
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
		return c, c.ops.RunInstall(InstallRequest{
			Lang: c.st.lang, Version: c.st.version, Pkgs: c.st.pkgs,
		})
	case "bootstrap":
		if !c.st.confirm {
			return c, nil // operator declined at the confirm
		}
		c.running = "bootstrap"
		return c, c.ops.RunBootstrap()
	}
	return c, nil
}

func (c *Console) installForm() *huh.Form {
	c.st = &formState{lang: "python"}
	// Two languages, packages-first: the operator names the tools they want; the
	// runtime is installed for them as needed (a bare runtime is a CLI concern).
	return huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("language").Value(&c.st.lang).Options(
			huh.NewOption("Python", "python"),
			huh.NewOption("JavaScript", "javascript"),
			huh.NewOption("Java", "java"),
		),
		huh.NewInput().Title("packages").
			Placeholder("e.g. requests / figlet cli-table3 / com.google.guava:guava:33.0.0-jre").
			Value(&c.st.pkgs).Validate(required("at least one package")),
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

func (c *Console) bootstrapForm() *huh.Form {
	c.st = &formState{}
	return huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title("Re-provision this sandbox?").
			Description("Ensure the protocol tree, pip.conf, and NANA.md, then run the ping/pong smoke test. Idempotent — existing runtimes and approvals are kept.").
			Value(&c.st.confirm),
	))
}

func (c Console) formTitle() string {
	if c.formKind == "bootstrap" {
		return "Bootstrap"
	}
	return "Install"
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
	case activity.KindServiced:
		c.served++
	case activity.KindApproved:
		c.approved++
	case activity.KindDenied:
		c.denied++
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
