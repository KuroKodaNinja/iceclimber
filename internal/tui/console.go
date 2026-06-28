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
// filled from the console's install form and handed to the OpRunner.
type InstallRequest struct {
	Lang    string // "python" | "node" | "pip" | "npm"
	Version string // runtime version (python/node) or target runtime (pip/npm)
	Pkgs    string // space/comma-separated specs (pip/npm only)
	Tier    string // "auto" | "mirror" | "relay" (pip/npm only)
}

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
}

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
	width     int
	height    int

	// form-bound values
	fLang, fVersion, fPkgs, fTier string
	fConfirm                      bool
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
		}
	case activity.Event:
		c.applyEvent(msg)
		return c, c.waitEvent()
	case *ApprovalRequest:
		c.modal = msg
		return c, c.waitEvent()
	case OpResultMsg:
		c.running = ""
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

func (c Console) View() string {
	if c.modal != nil {
		return modalView(c.width, c.height, c.modal)
	}
	if c.form != nil {
		return formView(c.width, c.height, c.formTitle(), c.form.View())
	}
	return dashboard(c.width, c.height, c.sandboxID, c.served, c.approved, c.denied,
		c.lastTS, true, c.popoLines, c.nanaLines, c.nana != nil, c.ops != nil, c.running)
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
	switch kind {
	case "install":
		c.running = c.fLang + ".install"
		return c, c.ops.RunInstall(InstallRequest{
			Lang: c.fLang, Version: c.fVersion, Pkgs: c.fPkgs, Tier: c.fTier,
		})
	case "bootstrap":
		if !c.fConfirm {
			return c, nil // operator declined at the confirm
		}
		c.running = "bootstrap"
		return c, c.ops.RunBootstrap()
	}
	return c, nil
}

func (c *Console) installForm() *huh.Form {
	c.fLang, c.fVersion, c.fPkgs, c.fTier = "node", "", "", "auto"
	// huh hides at the group level, so packages+tier (pip/npm only) live in their
	// own group, skipped when a runtime (python/node) is selected.
	isRuntime := func() bool { return c.fLang == "python" || c.fLang == "node" }
	return huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().Title("language").Value(&c.fLang).Options(
				huh.NewOption("node", "node"),
				huh.NewOption("python", "python"),
				huh.NewOption("npm", "npm"),
				huh.NewOption("pip", "pip"),
			),
			huh.NewInput().Title("runtime version").Placeholder("e.g. 24 / 3.12").Value(&c.fVersion).
				Validate(required("a version")),
		),
		huh.NewGroup(
			huh.NewInput().Title("packages").Placeholder("e.g. figlet cli-table3").Value(&c.fPkgs).
				Validate(required("at least one package")),
			huh.NewSelect[string]().Title("tier").Value(&c.fTier).Options(
				huh.NewOption("auto", "auto"),
				huh.NewOption("mirror", "mirror"),
				huh.NewOption("relay", "relay"),
			),
		).WithHideFunc(isRuntime),
	)
}

func (c *Console) bootstrapForm() *huh.Form {
	c.fConfirm = false
	return huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title("Re-provision this sandbox?").
			Description("Ensure the protocol tree, pip.conf, and NANA.md, then run the ping/pong smoke test. Idempotent — existing runtimes and approvals are kept.").
			Value(&c.fConfirm),
	))
}

func (c Console) formTitle() string {
	if c.formKind == "bootstrap" {
		return "Bootstrap"
	}
	return "Install"
}

func (c *Console) applyEvent(e activity.Event) {
	switch e.Kind {
	case activity.KindServiced:
		c.served++
	case activity.KindApproved:
		c.approved++
	case activity.KindDenied:
		c.denied++
	}
	if t, err := time.Parse(time.RFC3339, e.TS); err == nil {
		c.lastTS = t
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
