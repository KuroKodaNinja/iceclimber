package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// HostKeyInfo is what the first-connect trust prompt shows the operator.
type HostKeyInfo struct {
	SandboxID   string
	Address     string // host:port
	KeyType     string
	Fingerprint string // SHA256:…
	Mismatch    bool   // a different key is already recorded
}

// TrustPrompt is a small full-screen modal shown before the console when the
// sandbox's host key is not yet trusted. The operator eyeballs the fingerprint and
// accepts (y) or declines (n) — the in-TUI equivalent of recording a known_hosts
// entry, so first contact with an (ephemeral) sandbox stays inside the workflow.
type TrustPrompt struct {
	info     HostKeyInfo
	accepted bool
	w, h     int
}

// NewTrustPrompt builds the prompt for the given host key.
func NewTrustPrompt(info HostKeyInfo) TrustPrompt { return TrustPrompt{info: info} }

func (m TrustPrompt) Init() tea.Cmd { return nil }

func (m TrustPrompt) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "y", "Y":
			m.accepted = true
			return m, tea.Quit
		case "n", "N", "q", "esc", "ctrl+c":
			m.accepted = false
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m TrustPrompt) View() string {
	var b strings.Builder
	title := "Unknown sandbox host key"
	border := cWarn
	if m.info.Mismatch {
		title = "⚠ HOST KEY CHANGED"
		border = cErr
	}
	fmt.Fprintf(&b, "%s\n\n", lipgloss.NewStyle().Bold(true).Foreground(border).Render(title))
	fmt.Fprintf(&b, "sandbox:     %s\n", m.info.SandboxID)
	fmt.Fprintf(&b, "address:     %s\n", m.info.Address)
	fmt.Fprintf(&b, "key type:    %s\n", m.info.KeyType)
	fmt.Fprintf(&b, "fingerprint: %s\n\n", lipgloss.NewStyle().Bold(true).Render(m.info.Fingerprint))
	if m.info.Mismatch {
		fmt.Fprintf(&b, "%s\n\n", warnStyle.Render(
			"A different key is already recorded. Expected after a rebuild — but also\nexactly what a man-in-the-middle looks like. Accept only if you expected it."))
	} else {
		fmt.Fprintf(&b, "%s\n\n", dimStyle.Render("Verify this matches the key your sandbox actually has, then accept."))
	}
	fmt.Fprintf(&b, "%s   %s",
		okStyle.Render("[y] trust & record"),
		errStyle.Render("[n] abort"))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(1, 3).
		Render(b.String())
	if m.w == 0 || m.h == 0 {
		return box
	}
	return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, box)
}

// Accepted reports whether the operator chose to trust the key.
func (m TrustPrompt) Accepted() bool { return m.accepted }
