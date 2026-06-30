// Package tui is the live observability dashboard — a split-pane cockpit that
// renders Popo's activity log ([POPO]) beside the sandbox agent's output stream
// ([NANA]). It reads the same structured activity JSONL as `iceclimber logs` (via
// internal/tail), so it's a presentation layer over data Popo already records.
package tui

import (
	"encoding/json"
	"regexp"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/tail"
)

const maxLines = 500

type tickMsg time.Time

// Model is the dashboard state.
type Model struct {
	sandboxID string
	popo      *tail.Reader
	nana      *tail.Reader // nil when no --agent-log
	popoLines []popoLine
	nanaLines []string
	served    int
	approved  int
	denied    int
	lastTS    time.Time
	width     int
	height    int
}

type popoLine struct {
	plain string
	style lipgloss.Style
}

// New builds a dashboard reading the activity log and (optionally) the agent
// stream, seeded with their current history.
func New(sandboxID, activityPath, agentLog string) Model {
	m := Model{sandboxID: sandboxID, popo: tail.NewReader(activityPath), width: 100, height: 30}
	if agentLog != "" {
		m.nana = tail.NewReader(agentLog)
	}
	for _, l := range m.popo.History() {
		m.addPopo(l)
	}
	if m.nana != nil {
		for _, l := range tail.LastN(m.nana.History(), maxLines) {
			m.addNana(l)
		}
	}
	return m
}

func (m *Model) addPopo(raw string) {
	var e activity.Event
	if json.Unmarshal([]byte(raw), &e) != nil {
		return
	}
	switch e.Kind {
	case activity.KindServiced:
		m.served++
	case activity.KindApproved:
		m.approved++
	case activity.KindDenied:
		m.denied++
	}
	if t, err := time.Parse(time.RFC3339, e.TS); err == nil {
		m.lastTS = t
	}
	m.popoLines = append(m.popoLines, eventToLine(e))
	if len(m.popoLines) > maxLines {
		m.popoLines = m.popoLines[len(m.popoLines)-maxLines:]
	}
}

func (m *Model) addNana(raw string) {
	m.nanaLines = append(m.nanaLines, stripANSI(raw))
	if len(m.nanaLines) > maxLines {
		m.nanaLines = m.nanaLines[len(m.nanaLines)-maxLines:]
	}
}

// View renders the passive dashboard frame.
func (m Model) View() string {
	return dashboard(m.width, m.height, m.sandboxID, m.served, m.approved, m.denied,
		m.lastTS, ConnViewing, hbStatus{}, m.popoLines, m.nanaLines, m.nana != nil, m.nana != nil, false, "", "")
}

// Init starts the poll ticker.
func (m Model) Init() tea.Cmd { return tick() }

func tick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update polls the logs on each tick and handles resize/quit.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}
	case tickMsg:
		for _, l := range m.popo.Poll() {
			m.addPopo(l)
		}
		if m.nana != nil {
			for _, l := range m.nana.Poll() {
				m.addNana(l)
			}
		}
		return m, tick()
	}
	return m, nil
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

func shortTime(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.Local().Format("15:04:05")
	}
	return ts
}

// truncate clamps s to w display cells (rune-aware; plain text only).
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w <= 1 {
		return string(r[:w])
	}
	return string(r[:w-1]) + "…"
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t).Round(time.Second)
	if d <= 0 {
		return "just now"
	}
	return d.String() + " ago"
}
