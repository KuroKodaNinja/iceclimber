package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
)

var (
	cPopo  = lipgloss.Color("39")  // blue
	cNana  = lipgloss.Color("42")  // green
	cErr   = lipgloss.Color("203") // red
	cWarn  = lipgloss.Color("214") // amber
	cDim   = lipgloss.Color("243") // grey
	cTitle = lipgloss.Color("231") // near-white

	dimStyle   = lipgloss.NewStyle().Foreground(cDim)
	okStyle    = lipgloss.NewStyle().Foreground(cNana)
	errStyle   = lipgloss.NewStyle().Foreground(cErr)
	warnStyle  = lipgloss.NewStyle().Foreground(cWarn)
	plainStyle = lipgloss.NewStyle()
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(cTitle)
)

// eventToLine renders one activity event into a plain string + a colour by kind/status.
func eventToLine(e activity.Event) popoLine {
	ts := shortTime(e.TS)
	switch e.Kind {
	case activity.KindApproved:
		return popoLine{plain: fmt.Sprintf("%s  ✓ approved %s", ts, e.Detail), style: okStyle}
	case activity.KindDenied:
		return popoLine{plain: fmt.Sprintf("%s  ✗ denied %s", ts, e.Detail), style: errStyle}
	case activity.KindServiced:
		typ := e.Type
		if typ == "" {
			typ = "?"
		}
		plain := strings.TrimRight(fmt.Sprintf("%s  %-15s %-19s %s", ts, typ, e.Status, e.Detail), " ")
		st := plainStyle
		switch e.Status {
		case "error":
			st = errStyle
		case "needs_clarification":
			st = warnStyle
		}
		return popoLine{plain: plain, style: st}
	default:
		return popoLine{plain: fmt.Sprintf("%s  %s %s", ts, e.Kind, e.Detail), style: dimStyle}
	}
}

// View renders the dashboard frame.
func (m Model) View() string {
	w, h := m.width, m.height
	if w < 40 {
		w = 40
	}
	if h < 10 {
		h = 10
	}
	header := m.header(w)
	footer := m.footer(w)
	bodyH := h - lipgloss.Height(header) - lipgloss.Height(footer)
	if bodyH < 4 {
		bodyH = 4
	}

	if m.nana != nil {
		lw := (w - 1) / 2
		rw := w - 1 - lw
		body := lipgloss.JoinHorizontal(lipgloss.Top, m.popoPane(lw, bodyH), " ", m.nanaPane(rw, bodyH))
		return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, m.popoPane(w, bodyH), footer)
}

func (m Model) header(w int) string {
	left := titleStyle.Render(" iceclimber ▸ " + m.sandboxID + " ")
	right := dimStyle.Render(fmt.Sprintf("serviced %d · approved %d · denied %d · last %s ",
		m.served, m.approved, m.denied, ago(m.lastTS)))
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return lipgloss.NewStyle().Width(w).Render(left + strings.Repeat(" ", gap) + right)
}

func (m Model) footer(w int) string {
	return dimStyle.Width(w).Render(" [POPO] Popo's activity   [NANA] the agent's stream   ·   q quit")
}

func (m Model) popoPane(w, h int) string {
	cw, ch := w-2, h-2 // room for the border
	if ch < 2 {
		ch = 2
	}
	rows := make([]string, 0, ch-1)
	for _, pl := range lastPopo(m.popoLines, ch-1) {
		rows = append(rows, pl.style.Render(truncate(pl.plain, cw)))
	}
	content := titleStyle.Render("[POPO] controller") + "\n" + strings.Join(rows, "\n")
	return paneBox(cPopo, cw, ch).Render(content)
}

func (m Model) nanaPane(w, h int) string {
	cw, ch := w-2, h-2
	if ch < 2 {
		ch = 2
	}
	title := titleStyle.Render("[NANA] sandbox agent")
	var content string
	if m.nana == nil {
		content = title + "\n" + dimStyle.Render("(no --agent-log)")
	} else {
		rows := make([]string, 0, ch-1)
		start := 0
		if n := len(m.nanaLines); n > ch-1 {
			start = n - (ch - 1)
		}
		for _, l := range m.nanaLines[start:] {
			rows = append(rows, truncate(l, cw))
		}
		content = title + "\n" + strings.Join(rows, "\n")
	}
	return paneBox(cNana, cw, ch).Render(content)
}

func paneBox(border lipgloss.Color, w, h int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Width(w).
		Height(h).
		MaxHeight(h + 2)
}

func lastPopo(lines []popoLine, n int) []popoLine {
	if n <= 0 || n >= len(lines) {
		return lines
	}
	return lines[len(lines)-n:]
}
