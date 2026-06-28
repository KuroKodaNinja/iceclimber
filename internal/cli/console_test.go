package cli

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/KuroKodaNinja/iceclimber/internal/tui"
)

func TestTuiAsker_Mapping(t *testing.T) {
	for reply, want := range map[int]choice{
		tui.Approve:    choiceApproveOnce,
		tui.ApproveAll: choiceApproveRemember,
		tui.Deny:       choiceDenyOnce,
		tui.DenyAll:    choiceDenyRemember,
	} {
		events := make(chan tea.Msg, 1)
		ta := &tuiAsker{events: events, done: make(chan struct{})}
		go func(r int) {
			req := (<-events).(*tui.ApprovalRequest)
			req.Reply <- r
		}(reply)
		if got := ta.ask(prompt{title: "x"}); got != want {
			t.Errorf("reply %d → %v, want %v", reply, got, want)
		}
	}
}

func TestTuiAsker_ShutdownDenies(t *testing.T) {
	done := make(chan struct{})
	close(done)
	ta := &tuiAsker{events: make(chan tea.Msg), done: done} // no reader
	if got := ta.ask(prompt{title: "x"}); got != choiceDenyOnce {
		t.Errorf("shutdown should fail safe to deny, got %v", got)
	}
}

func TestSplitSpecs(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 0},
		{"figlet", 1},
		{"figlet cli-table3", 2},
		{"figlet, cli-table3", 2},
		{"  figlet ,, cli-table3  ", 2},
	} {
		if got := splitSpecs(tc.in); len(got) != tc.want {
			t.Errorf("splitSpecs(%q) = %v, want %d items", tc.in, got, tc.want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"Python 3.12.13", "Python 3.12.13"},
		{"\n\n  v24.4.0  \nextra", "v24.4.0"},
		{"  \n", ""},
	} {
		if got := firstLine(tc.in); got != tc.want {
			t.Errorf("firstLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDefaultVersion(t *testing.T) {
	if defaultVersion("python", "") != "3.12" {
		t.Error("blank python version should default to 3.12")
	}
	if defaultVersion("javascript", "") != "24" {
		t.Error("blank javascript version should default to 24")
	}
	if defaultVersion("python", "3.11") != "3.11" {
		t.Error("an explicit version should be preserved")
	}
	if defaultVersion("javascript", "  22 ") != "22" {
		t.Error("an explicit version should be trimmed and preserved")
	}
}
