package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
)

// fakeOps records what the console asked it to run.
type fakeOps struct {
	install   *InstallRequest
	bootstrap bool
}

func (f *fakeOps) RunInstall(r InstallRequest) tea.Cmd {
	return func() tea.Msg { f.install = &r; return OpResultMsg{} }
}

func (f *fakeOps) RunBootstrap() tea.Cmd {
	return func() tea.Msg { f.bootstrap = true; return OpResultMsg{} }
}

func TestConsole_ActivityEvent(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg, 4), "", nil)
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

func TestConsole_OperatedEvent(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg, 4), "", nil)
	updated, _ := c.Update(activity.Event{
		TS: "2026-06-28T18:00:02Z", Kind: activity.KindOperated,
		Type: "node.install", Status: "ok", Detail: "node 24 at /home",
	})
	c2 := updated.(Console)
	// Operator actions show in the pane but aren't agent-serviced requests.
	if c2.served != 0 || len(c2.popoLines) != 1 {
		t.Fatalf("served=%d lines=%d, want 0/1", c2.served, len(c2.popoLines))
	}
}

func TestConsole_ApprovalModal(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg, 4), "", nil)
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
	c := NewConsole("sbx", make(chan tea.Msg, 1), "", nil)
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
	c := NewConsole("sbx", make(chan tea.Msg), "", nil)
	_, cmd := c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q should return a command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q should return the Quit command")
	}
}

func TestConsole_InstallKeyOpensForm(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg), "", &fakeOps{})
	updated, _ := c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	c2 := updated.(Console)
	if c2.form == nil || c2.formKind != "install" {
		t.Fatalf("'i' should open the install form, got form=%v kind=%q", c2.form != nil, c2.formKind)
	}
}

func TestConsole_MenuDisabledWithoutOps(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg), "", nil)
	updated, cmd := c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	if updated.(Console).form != nil {
		t.Error("install form must not open when no OpRunner is supplied")
	}
	if cmd != nil {
		t.Error("'i' should be a no-op without ops")
	}
}

func TestConsole_SubmitInstallRunsOp(t *testing.T) {
	ops := &fakeOps{}
	c := NewConsole("sbx", make(chan tea.Msg), "", ops)
	c.formKind = "install"
	c.fLang, c.fVersion, c.fPkgs, c.fTier = "npm", "24", "figlet", "auto"

	updated, cmd := c.submitForm("install")
	c2 := updated.(Console)
	if c2.running != "npm.install" {
		t.Fatalf("running = %q, want npm.install", c2.running)
	}
	if cmd == nil {
		t.Fatal("submit should return the op command")
	}
	if _, ok := cmd().(OpResultMsg); !ok {
		t.Error("op command should resolve to OpResultMsg")
	}
	if ops.install == nil || ops.install.Lang != "npm" || ops.install.Version != "24" || ops.install.Pkgs != "figlet" {
		t.Errorf("RunInstall got %+v", ops.install)
	}

	// The result clears the running indicator.
	done, _ := c2.Update(OpResultMsg{})
	if done.(Console).running != "" {
		t.Error("OpResultMsg should clear running")
	}
}

func TestConsole_BootstrapDeclinedRunsNothing(t *testing.T) {
	ops := &fakeOps{}
	c := NewConsole("sbx", make(chan tea.Msg), "", ops)
	c.formKind = "bootstrap"
	c.fConfirm = false

	updated, cmd := c.submitForm("bootstrap")
	if updated.(Console).running != "" || cmd != nil {
		t.Error("declining the bootstrap confirm should run nothing")
	}
	if ops.bootstrap {
		t.Error("RunBootstrap should not be called when declined")
	}
}

func TestConsole_RunningBlocksInput(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg), "", &fakeOps{})
	c.running = "node.install"
	_, cmd := c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd != nil {
		t.Error("keys should be ignored while an operator action is in flight")
	}
}
