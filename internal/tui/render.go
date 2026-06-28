package tui

import (
	"fmt"
	"strings"
	"time"

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

// dashboard renders the header + two panes + footer.
func dashboard(w, h int, sandboxID string, served, approved, denied int, lastTS time.Time, serving bool, popoLines []popoLine, nanaLines []string, hasNana bool) string {
	if w < 40 {
		w = 40
	}
	if h < 10 {
		h = 10
	}
	hdr := header(w, sandboxID, served, approved, denied, lastTS, serving)
	ftr := footer(w)
	bodyH := h - lipgloss.Height(hdr) - lipgloss.Height(ftr)
	if bodyH < 4 {
		bodyH = 4
	}
	if hasNana {
		lw := (w - 1) / 2
		rw := w - 1 - lw
		body := lipgloss.JoinHorizontal(lipgloss.Top, popoPane(lw, bodyH, popoLines), " ", nanaPane(rw, bodyH, true, nanaLines))
		return lipgloss.JoinVertical(lipgloss.Left, hdr, body, ftr)
	}
	return lipgloss.JoinVertical(lipgloss.Left, hdr, popoPane(w, bodyH, popoLines), ftr)
}

func header(w int, sandboxID string, served, approved, denied int, lastTS time.Time, serving bool) string {
	state := dimStyle.Render("○ viewing")
	if serving {
		state = okStyle.Render("● serving")
	}
	left := titleStyle.Render(" iceclimber ▸ "+sandboxID+" ") + " " + state
	right := dimStyle.Render(fmt.Sprintf("serviced %d · approved %d · denied %d · last %s ",
		served, approved, denied, ago(lastTS)))
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return lipgloss.NewStyle().Width(w).Render(left + strings.Repeat(" ", gap) + right)
}

func footer(w int) string {
	return dimStyle.Width(w).Render(" [POPO] Popo's activity   [NANA] the agent's stream   ·   q quit")
}

func popoPane(w, h int, lines []popoLine) string {
	cw, ch := w-2, h-2
	if ch < 2 {
		ch = 2
	}
	rows := make([]string, 0, ch-1)
	for _, pl := range lastPopo(lines, ch-1) {
		rows = append(rows, pl.style.Render(truncate(pl.plain, cw)))
	}
	content := titleStyle.Render("[POPO] controller") + "\n" + strings.Join(rows, "\n")
	return paneBox(cPopo, cw, ch).Render(content)
}

func nanaPane(w, h int, hasNana bool, lines []string) string {
	cw, ch := w-2, h-2
	if ch < 2 {
		ch = 2
	}
	title := titleStyle.Render("[NANA] sandbox agent")
	var content string
	if !hasNana {
		content = title + "\n" + dimStyle.Render("(no --agent-log)")
	} else {
		start := 0
		if n := len(lines); n > ch-1 {
			start = n - (ch - 1)
		}
		rows := make([]string, 0, ch-1)
		for _, l := range lines[start:] {
			rows = append(rows, truncate(l, cw))
		}
		content = title + "\n" + strings.Join(rows, "\n")
	}
	return paneBox(cNana, cw, ch).Render(content)
}

// modalView renders a centred approval modal over a blank screen.
func modalView(w, h int, req *ApprovalRequest) string {
	border := cWarn
	hdr := "Approve operation"
	if req.Kind == "egress" {
		border = cErr
		hdr = "Approve egress"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s · sandbox %s\n\n", titleStyle.Render(hdr), req.Sandbox)
	fmt.Fprintf(&b, "%s\n", lipgloss.NewStyle().Bold(true).Render(req.Title))
	for _, f := range req.Fields {
		fmt.Fprintf(&b, "  %-9s %s\n", dimStyle.Render(f[0]), f[1])
	}
	if req.Note != "" {
		fmt.Fprintf(&b, "\n%s\n", warnStyle.Render(req.Note))
	}
	fmt.Fprintf(&b, "\n%s\n", dimStyle.Render(fmt.Sprintf("[y] approve   [a] %s   [n] deny   [d] deny+remember", req.RememberLabel)))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(1, 2).
		MaxWidth(w - 4).
		Render(b.String())
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
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
