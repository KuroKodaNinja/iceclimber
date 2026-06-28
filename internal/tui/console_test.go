package tui

import (
	"strings"
	"testing"
	"time"

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
		t.Fatal("submit should return the op command")
	}
	if _, ok := cmd().(OpResultMsg); !ok {
		t.Error("op command should resolve to OpResultMsg")
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

// runCmd executes a tea.Cmd but gives up after a short delay, so timer-based cmds
// (huh's cursor blink) and blocking cmds (the console's waitEvent) are skipped while
// instantaneous navigation messages (huh's nextField/nextGroup) still flow.
func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case m := <-ch:
		return m
	case <-time.After(25 * time.Millisecond):
		return nil
	}
}

// driveKeys feeds key messages through the console and pumps the resulting cmds
// (breadth-first over batches) so huh's field/group transitions actually happen —
// approximating the Bubble Tea runtime closely enough to drive the real form.
func driveKeys(c Console, keys ...tea.Msg) Console {
	m := tea.Model(c)
	pump := func(cmd tea.Cmd) {
		queue := []tea.Cmd{cmd}
		for i := 0; i < 300 && len(queue) > 0; i++ {
			msg := runCmd(queue[0])
			queue = queue[1:]
			switch mm := msg.(type) {
			case nil:
			case tea.BatchMsg:
				queue = append(queue, mm...)
			default:
				var nc tea.Cmd
				m, nc = m.Update(msg)
				queue = append(queue, nc)
			}
		}
	}
	for _, k := range keys {
		var cmd tea.Cmd
		m, cmd = m.Update(k)
		pump(cmd)
	}
	return m.(Console)
}

func key(s string) tea.Msg       { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
func typeRunes(s string) tea.Msg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

var (
	enterKey = tea.KeyMsg{Type: tea.KeyEnter}
	downKey  = tea.KeyMsg{Type: tea.KeyDown}
)

// TestConsole_InstallFlow_Python drives the actual form: open, type packages,
// leave version blank, submit — asserting the request reaches the OpRunner.
func TestConsole_InstallFlow_Python(t *testing.T) {
	ops := &fakeOps{}
	c := NewConsole("sbx", make(chan tea.Msg), "", ops)
	// i → [language: Python default] enter → [packages] type → enter → [version] enter.
	c = driveKeys(c, key("i"), enterKey, typeRunes("requests"), enterKey, enterKey)
	if ops.install == nil {
		t.Fatal("install flow did not reach the OpRunner")
	}
	if ops.install.Lang != "python" || ops.install.Pkgs != "requests" || ops.install.Version != "" {
		t.Errorf("python flow request = %+v, want {python requests <blank>}", ops.install)
	}
}

// TestConsole_InstallFlow_JavaScript is the regression for the value-copy binding
// bug: selecting JavaScript must actually reach the request (it previously stayed
// Python because the form wrote to a stale Console copy).
func TestConsole_InstallFlow_JavaScript(t *testing.T) {
	ops := &fakeOps{}
	c := NewConsole("sbx", make(chan tea.Msg), "", ops)
	// i → [language] down→JavaScript, enter → [packages] type → enter → [version] type → enter.
	c = driveKeys(c, key("i"), downKey, enterKey, typeRunes("figlet"), enterKey, typeRunes("24"), enterKey)
	if ops.install == nil {
		t.Fatal("javascript flow did not reach the OpRunner")
	}
	if ops.install.Lang != "javascript" {
		t.Errorf("selecting JavaScript must reach the request; got Lang=%q (binding bug?)", ops.install.Lang)
	}
	if ops.install.Pkgs != "figlet" || ops.install.Version != "24" {
		t.Errorf("javascript flow request = %+v, want {javascript figlet 24}", ops.install)
	}
}

// TestConsole_InstallFlow_RequiresPackages: submitting with no packages must not
// fire an install (the field is required).
func TestConsole_InstallFlow_RequiresPackages(t *testing.T) {
	ops := &fakeOps{}
	c := NewConsole("sbx", make(chan tea.Msg), "", ops)
	c = driveKeys(c, key("i"), enterKey, enterKey, enterKey, enterKey)
	if ops.install != nil {
		t.Errorf("blank packages must not install; got %+v", ops.install)
	}
	if c.form == nil {
		t.Error("the form should stay open on a validation error")
	}
}

// TestConsole_BootstrapFlow drives the confirm form and asserts RunBootstrap fires.
func TestConsole_BootstrapFlow(t *testing.T) {
	ops := &fakeOps{}
	c := NewConsole("sbx", make(chan tea.Msg), "", ops)
	// b → confirm defaults to "no"; left/"yes" then enter.
	c = driveKeys(c, key("b"), key("y"), enterKey)
	if !ops.bootstrap {
		t.Error("confirming bootstrap should call RunBootstrap")
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
