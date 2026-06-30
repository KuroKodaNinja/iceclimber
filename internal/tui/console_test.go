package tui

import (
	"errors"
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
	f.install = &r // recorded at call time (submitForm now batches the op with a spinner tick)
	return func() tea.Msg { return OpResultMsg{} }
}

func (f *fakeOps) RunBootstrap() tea.Cmd {
	return func() tea.Msg { f.bootstrap = true; return OpResultMsg{} }
}
func (f *fakeOps) PollStatus() tea.Cmd          { return func() tea.Msg { return StatusMsg{} } }
func (f *fakeOps) Egress() EgressSnapshot       { return EgressSnapshot{} }
func (f *fakeOps) ApprovePending(string) error  { return nil }
func (f *fakeOps) DenyPending(string) error     { return nil }
func (f *fakeOps) ForgetRule(_, _ string) error { return nil }

// errOps fails egress actions and serves a fixed snapshot, for error-surfacing.
type errOps struct {
	fakeOps
	eg EgressSnapshot
}

func (e *errOps) Egress() EgressSnapshot       { return e.eg }
func (e *errOps) ForgetRule(_, _ string) error { return errors.New("disk full") }
func (e *errOps) ApprovePending(string) error  { return errors.New("write failed") }

// A failed egress action must be shown to the operator, not swallowed.
func TestConsole_EgressActionErrorSurfaced(t *testing.T) {
	ops := &errOps{eg: EgressSnapshot{Rules: []EgressRule{{Kind: "allow", Pattern: "https://x/*"}}}}
	c := NewConsole("sbx", make(chan tea.Msg), "", ops)
	c.panel, c.egress, c.cursor = "egress", ops.eg, 0

	updated, _ := c.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	c2 := updated.(Console)
	if c2.panelErr == "" {
		t.Fatal("a failed forget should surface an error in the panel, not be swallowed")
	}
	if !strings.Contains(c2.View(), "disk full") {
		t.Errorf("egress view should render the error:\n%s", c2.View())
	}
}

// The status panel must show an unreachable sandbox, not a stale/blank snapshot.
func TestStatusViewShowsError(t *testing.T) {
	out := statusView(80, 24, "sbx", &StatusSnapshot{Err: "connection refused"})
	if !strings.Contains(out, "unreachable") || !strings.Contains(out, "connection refused") {
		t.Errorf("status view should show the connection error:\n%s", out)
	}
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

func TestConsole_SandboxEchoRoutesToNana(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg, 4), "", nil)
	updated, cmd := c.Update(activity.Event{
		TS: "2026-06-28T18:00:03Z", Kind: activity.KindVerified, Side: activity.SideNana,
		Status: "ok", Detail: "python 3.12.13",
	})
	c2 := updated.(Console)
	if len(c2.popoLines) != 0 {
		t.Errorf("a sandbox echo must not appear in the POPO pane, got %d lines", len(c2.popoLines))
	}
	if len(c2.nanaLines) != 1 {
		t.Fatalf("a sandbox echo should land in the NANA pane, got %d lines", len(c2.nanaLines))
	}
	if !strings.Contains(c2.nanaLines[0], "✓") || !strings.Contains(c2.nanaLines[0], "python 3.12.13") {
		t.Errorf("nana line = %q", c2.nanaLines[0])
	}
	if c2.served != 0 {
		t.Errorf("a sandbox echo is not a serviced request; served = %d, want 0", c2.served)
	}
	if cmd == nil {
		t.Error("an event should re-arm the event listener")
	}
}

func TestConsole_FailedEchoMarked(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg, 4), "", nil)
	updated, _ := c.Update(activity.Event{
		TS: "2026-06-28T18:00:04Z", Kind: activity.KindVerified, Side: activity.SideNana,
		Status: "failed", Detail: "requests not present",
	})
	c2 := updated.(Console)
	if len(c2.nanaLines) != 1 || !strings.Contains(c2.nanaLines[0], "✗") {
		t.Errorf("failed echo should be marked ✗, got %v", c2.nanaLines)
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
	c.st = &formState{lang: "javascript", version: "24", pkgs: "figlet"}

	updated, cmd := c.submitForm("install")
	c2 := updated.(Console)
	if c2.running != "JavaScript install" {
		t.Fatalf("running = %q, want JavaScript install", c2.running)
	}
	if cmd == nil {
		t.Fatal("submit should return the op command (batched with the spinner tick)")
	}
	if ops.install == nil || ops.install.Lang != "javascript" ||
		ops.install.Version != "24" || ops.install.Pkgs != "figlet" {
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
	c.st = &formState{confirm: false}

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
