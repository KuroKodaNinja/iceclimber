package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
)

func TestConsole_ActivityEvent(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg, 4), "")
	updated, cmd := c.Update(activity.Event{
		TS: "2026-06-28T18:00:01Z", Kind: activity.KindServiced,
		Type: "python.install", Status: "ok", Detail: "python 3.12.13",
	})
	c2 := updated.(Console)
	if c2.served != 1 || len(c2.popoLines) != 1 {
		t.Fatalf("served=%d lines=%d, want 1/1", c2.served, len(c2.popoLines))
	}
	if cmd == nil {
		t.Error("an event should re-arm the event listener")
	}
}

func TestConsole_ApprovalModal(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg, 4), "")
	reply := make(chan int, 1)
	req := &ApprovalRequest{Sandbox: "sbx", Title: "web.fetch GET", Kind: "egress", Reply: reply}

	updated, _ := c.Update(req)
	c2 := updated.(Console)
	if c2.modal == nil {
		t.Fatal("modal should be set on ApprovalRequest")
	}
	if v := c2.View(); !strings.Contains(v, "web.fetch GET") || !strings.Contains(v, "Approve egress") {
		t.Errorf("modal view missing content:\n%s", v)
	}

	// Answer 'a' (approve + remember).
	updated2, _ := c2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	c3 := updated2.(Console)
	if c3.modal != nil {
		t.Error("modal should clear after the operator answers")
	}
	select {
	case got := <-reply:
		if got != ApproveAll {
			t.Errorf("reply = %d, want ApproveAll", got)
		}
	default:
		t.Error("no choice sent on Reply")
	}
}

func TestConsole_ModalIgnoresNonChoiceKeys(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg, 1), "")
	c.modal = &ApprovalRequest{Reply: make(chan int, 1)}
	updated, cmd := c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if updated.(Console).modal == nil {
		t.Error("a held operation must stay until answered (q should not dismiss it)")
	}
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Error("q must not quit while a modal is active")
		}
	}
}

func TestConsole_Quit(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg), "")
	_, cmd := c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q should return a command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q should return the Quit command")
	}
}
