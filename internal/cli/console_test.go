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
