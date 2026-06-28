package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/tail"
)

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

// Console is the embed-serve operator console: it renders the live activity feed
// ([POPO]) and the agent stream ([NANA]), and surfaces approval modals fed from the
// dispatcher. It is fed by an event channel carrying activity.Event (serviced
// requests) and *ApprovalRequest (held operations).
type Console struct {
	sandboxID string
	events    <-chan tea.Msg
	nana      *tail.Reader
	popoLines []popoLine
	nanaLines []string
	served    int
	approved  int
	denied    int
	lastTS    time.Time
	modal     *ApprovalRequest
	width     int
	height    int
}

// NewConsole builds a console reading events (and, optionally, the agent stream).
func NewConsole(sandboxID string, events <-chan tea.Msg, agentLog string) Console {
	c := Console{sandboxID: sandboxID, events: events, width: 100, height: 30}
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
	case tea.KeyMsg:
		if c.modal != nil {
			// A held operation must be answered — only y/a/n/d apply.
			if ch, ok := modalKey(msg.String()); ok {
				c.modal.Reply <- ch
				c.modal = nil
			}
			return c, nil
		}
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return c, tea.Quit
		}
	case activity.Event:
		c.applyEvent(msg)
		return c, c.waitEvent()
	case *ApprovalRequest:
		c.modal = msg
		return c, c.waitEvent()
	case tickMsg:
		if c.nana != nil {
			for _, l := range c.nana.Poll() {
				c.addNana(l)
			}
		}
		return c, tick()
	}
	return c, nil
}

func (c Console) View() string {
	if c.modal != nil {
		return modalView(c.width, c.height, c.modal)
	}
	return dashboard(c.width, c.height, c.sandboxID, c.served, c.approved, c.denied,
		c.lastTS, true, c.popoLines, c.nanaLines, c.nana != nil)
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
