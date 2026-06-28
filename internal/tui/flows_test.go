package tui

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
)

// These tests drive the console through the REAL Bubble Tea runtime (teatest):
// keys and messages go through the genuine event loop and huh form, and we assert
// on rendered output and on the operator actions that fired. This is the TUI
// analogue of the stdin/stdout functional tests the CLI had — every interactive
// flow is exercised end to end, not approximated.
//
// huh advances fields via async cmd round-trips, so before typing into a field we
// wait for its placeholder to render (proof the field is focused and the previous
// transition settled). Typed values are chosen not to collide with placeholders.

// recordOps records operator actions over channels so the test (on a different
// goroutine from the program) can assert what fired without data races.
type recordOps struct {
	install   chan InstallRequest
	bootstrap chan struct{}
}

func newRecordOps() *recordOps {
	return &recordOps{install: make(chan InstallRequest, 1), bootstrap: make(chan struct{}, 1)}
}

func (o *recordOps) RunInstall(r InstallRequest) tea.Cmd {
	return func() tea.Msg { o.install <- r; return OpResultMsg{} }
}
func (o *recordOps) RunBootstrap() tea.Cmd {
	return func() tea.Msg { o.bootstrap <- struct{}{}; return OpResultMsg{} }
}

func startConsole(t *testing.T, ops OpRunner) *teatest.TestModel {
	t.Helper()
	return teatest.NewTestModel(t, NewConsole("sbx", make(chan tea.Msg), "", ops),
		teatest.WithInitialTermSize(120, 40))
}

func waitText(t *testing.T, tm *teatest.TestModel, sub string) {
	t.Helper()
	waitAll(t, tm, sub)
}

// waitAll blocks until all substrings have appeared. teatest's WaitFor accumulates
// within one call but drains the shared reader across calls, so substrings from the
// same render must be checked together (not in successive waitText calls).
func waitAll(t *testing.T, tm *teatest.TestModel, subs ...string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		for _, s := range subs {
			if !bytes.Contains(b, []byte(s)) {
				return false
			}
		}
		return true
	}, teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(15*time.Millisecond))
}

func send(tm *teatest.TestModel, t tea.KeyType) { tm.Send(tea.KeyMsg{Type: t}) }
func press(tm *teatest.TestModel, s string) {
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
}

func finalConsole(t *testing.T, tm *teatest.TestModel) Console {
	t.Helper()
	tm.Quit()
	return tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(Console)
}

// --- Dashboard / chrome -----------------------------------------------------

func TestFlow_DashboardChrome(t *testing.T) {
	tm := startConsole(t, newRecordOps())
	// Header, both panes, and the footer management keys render in one frame.
	waitAll(t, tm, "iceclimber ▸ sbx", "[POPO]", "[NANA]", "i install", "b bootstrap")
	tm.Quit()
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}

func TestFlow_MenuHiddenWithoutOps(t *testing.T) {
	tm := startConsole(t, nil) // no OpRunner ⇒ no management menu
	waitText(t, tm, "q quit")
	press(tm, "i") // must be a no-op
	tm.Quit()
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
	out, _ := io.ReadAll(tm.FinalOutput(t))
	if bytes.Contains(out, []byte("i install")) {
		t.Error("footer must not advertise install without an OpRunner")
	}
	if bytes.Contains(out, []byte("packages")) {
		t.Error("'i' must not open the install form without an OpRunner")
	}
}

// --- Live activity ----------------------------------------------------------

func TestFlow_ServicedEventToPopo(t *testing.T) {
	tm := startConsole(t, nil)
	tm.Send(activity.Event{
		TS: "2026-06-28T18:00:01Z", Kind: activity.KindServiced,
		Type: "pip.install", Status: "ok", Detail: "requests 2.32.3",
	})
	waitText(t, tm, "pip.install") // type renders early in the (narrow) POPO line
	c := finalConsole(t, tm)
	if c.served != 1 {
		t.Errorf("served = %d, want 1", c.served)
	}
	if len(c.popoLines) != 1 || !strings.Contains(c.popoLines[0].plain, "requests 2.32.3") {
		t.Errorf("serviced event should be the POPO line; got %+v", c.popoLines)
	}
	if len(c.nanaLines) != 0 {
		t.Errorf("a serviced event must not land in [NANA]; got %v", c.nanaLines)
	}
}

func TestFlow_SandboxEchoToNana(t *testing.T) {
	tm := startConsole(t, nil)
	tm.Send(activity.Event{
		TS: "2026-06-28T18:00:02Z", Kind: activity.KindVerified, Side: activity.SideNana,
		Status: "ok", Detail: "python 3.12.13",
	})
	waitText(t, tm, "✓ python 3.12.13")
	c := finalConsole(t, tm)
	if len(c.nanaLines) != 1 {
		t.Errorf("echo should land in [NANA]; got %v", c.nanaLines)
	}
	if len(c.popoLines) != 0 || c.served != 0 {
		t.Errorf("echo must not touch [POPO]/counters; popo=%v served=%d", c.popoLines, c.served)
	}
}

// --- Approval modal ---------------------------------------------------------

func TestFlow_EgressModalApprove(t *testing.T) {
	tm := startConsole(t, nil)
	reply := make(chan int, 1)
	tm.Send(&ApprovalRequest{
		Sandbox: "sbx", Kind: "egress", Title: "web.fetch GET",
		Fields: [][2]string{{"url", "https://example.com"}}, RememberLabel: "remember host example.com", Reply: reply,
	})
	waitAll(t, tm, "Approve egress", "web.fetch GET")
	press(tm, "a") // approve + remember
	if got := <-reply; got != ApproveAll {
		t.Errorf("reply = %d, want ApproveAll", got)
	}
	c := finalConsole(t, tm)
	if c.modal != nil {
		t.Error("modal should clear after answering")
	}
}

func TestFlow_OperationModalDeny(t *testing.T) {
	tm := startConsole(t, nil)
	reply := make(chan int, 1)
	tm.Send(&ApprovalRequest{Sandbox: "sbx", Kind: "operation", Title: "pip.install requests", Reply: reply})
	waitText(t, tm, "Approve operation")
	press(tm, "n")
	if got := <-reply; got != Deny {
		t.Errorf("reply = %d, want Deny", got)
	}
	finalConsole(t, tm)
}

func TestFlow_ModalPreemptsQuit(t *testing.T) {
	tm := startConsole(t, nil)
	reply := make(chan int, 1)
	tm.Send(&ApprovalRequest{Sandbox: "sbx", Kind: "egress", Title: "web.fetch GET", Reply: reply})
	waitText(t, tm, "Approve egress")
	press(tm, "q") // must NOT quit while a held operation waits
	press(tm, "y") // still answerable ⇒ proves the program is alive and the modal held
	if got := <-reply; got != Approve {
		t.Errorf("reply = %d, want Approve (q should not have dismissed/quit)", got)
	}
	finalConsole(t, tm)
}

// --- Install form -----------------------------------------------------------

func TestFlow_InstallPython(t *testing.T) {
	ops := newRecordOps()
	tm := startConsole(t, ops)
	waitText(t, tm, "i install")
	press(tm, "i")
	waitText(t, tm, "language")
	send(tm, tea.KeyEnter) // accept Python (default), advance to packages
	waitText(t, tm, "requests / figlet")
	tm.Type("httpx")
	waitText(t, tm, "httpx")
	send(tm, tea.KeyEnter) // advance to version
	waitText(t, tm, "3.12 / 24")
	send(tm, tea.KeyEnter) // version blank ⇒ submit
	select {
	case r := <-ops.install:
		if r.Lang != "python" || r.Pkgs != "httpx" || r.Version != "" {
			t.Fatalf("request = %+v, want {python httpx <blank>}", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("python install never fired")
	}
	finalConsole(t, tm)
}

// TestFlow_InstallJavaScript is the regression for the value-copy binding bug:
// selecting JavaScript must actually reach the request.
func TestFlow_InstallJavaScript(t *testing.T) {
	ops := newRecordOps()
	tm := startConsole(t, ops)
	waitText(t, tm, "i install")
	press(tm, "i")
	waitText(t, tm, "JavaScript")
	send(tm, tea.KeyDown)  // move selection to JavaScript
	send(tm, tea.KeyEnter) // accept, advance to packages
	waitText(t, tm, "requests / figlet")
	tm.Type("express")
	waitText(t, tm, "express")
	send(tm, tea.KeyEnter) // advance to version
	waitText(t, tm, "3.12 / 24")
	tm.Type("20")
	waitText(t, tm, "20")
	send(tm, tea.KeyEnter) // submit
	select {
	case r := <-ops.install:
		if r.Lang != "javascript" {
			t.Fatalf("selecting JavaScript must reach the request; got Lang=%q (binding bug)", r.Lang)
		}
		if r.Pkgs != "express" || r.Version != "20" {
			t.Fatalf("request = %+v, want {javascript express 20}", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("javascript install never fired")
	}
	finalConsole(t, tm)
}

func TestFlow_InstallRequiresPackages(t *testing.T) {
	ops := newRecordOps()
	tm := startConsole(t, ops)
	press(tm, "i")
	waitText(t, tm, "language")
	send(tm, tea.KeyEnter) // advance to packages
	waitText(t, tm, "requests / figlet")
	send(tm, tea.KeyEnter)                        // submit with no packages
	waitText(t, tm, "enter at least one package") // validation error shows
	select {
	case r := <-ops.install:
		t.Fatalf("blank packages must not install; got %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
	c := finalConsole(t, tm)
	if c.form == nil {
		t.Error("form should stay open on a validation error")
	}
}

func TestFlow_InstallAbort(t *testing.T) {
	ops := newRecordOps()
	tm := startConsole(t, ops)
	press(tm, "i")
	waitText(t, tm, "language")
	send(tm, tea.KeyCtrlC)       // huh treats ctrl+c as abort (form-level), not program quit
	waitText(t, tm, "i install") // dashboard footer is back
	select {
	case r := <-ops.install:
		t.Fatalf("aborting must not install; got %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
	c := finalConsole(t, tm)
	if c.form != nil {
		t.Error("form should be cleared after abort")
	}
}

// --- Bootstrap form ---------------------------------------------------------

func TestFlow_BootstrapConfirm(t *testing.T) {
	ops := newRecordOps()
	tm := startConsole(t, ops)
	press(tm, "b")
	waitText(t, tm, "Re-provision this sandbox?")
	press(tm, "y") // accept
	select {
	case <-ops.bootstrap:
	case <-time.After(5 * time.Second):
		t.Fatal("confirming bootstrap should call RunBootstrap")
	}
	finalConsole(t, tm)
}

func TestFlow_BootstrapDecline(t *testing.T) {
	ops := newRecordOps()
	tm := startConsole(t, ops)
	press(tm, "b")
	waitText(t, tm, "Re-provision this sandbox?")
	press(tm, "n") // decline
	select {
	case <-ops.bootstrap:
		t.Fatal("declining must not bootstrap")
	case <-time.After(300 * time.Millisecond):
	}
	finalConsole(t, tm)
}

// --- Running indicator ------------------------------------------------------

// gateOps blocks RunInstall until released, so the running indicator stays visible.
type gateOps struct{ release chan struct{} }

func (o *gateOps) RunInstall(InstallRequest) tea.Cmd {
	return func() tea.Msg { <-o.release; return OpResultMsg{} }
}
func (o *gateOps) RunBootstrap() tea.Cmd {
	return func() tea.Msg { <-o.release; return OpResultMsg{} }
}

func TestFlow_RunningIndicator(t *testing.T) {
	ops := &gateOps{release: make(chan struct{})}
	tm := startConsole(t, ops)
	press(tm, "i")
	waitText(t, tm, "language")
	send(tm, tea.KeyEnter) // advance to packages
	waitText(t, tm, "requests / figlet")
	tm.Type("six")
	waitText(t, tm, "six")
	send(tm, tea.KeyEnter) // advance to version
	waitText(t, tm, "3.12 / 24")
	send(tm, tea.KeyEnter) // submit ⇒ RunInstall blocks
	waitText(t, tm, "running Python install")
	press(tm, "i") // input is ignored while running (no second form)
	close(ops.release)
	tm.Quit() // after release the op resolves; Quit forces a clean finish
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}

// --- Quit -------------------------------------------------------------------

func TestFlow_QuitKey(t *testing.T) {
	tm := startConsole(t, nil)
	waitText(t, tm, "q quit")
	press(tm, "q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}

func TestFlow_QuitCtrlC(t *testing.T) {
	tm := startConsole(t, nil)
	waitText(t, tm, "q quit")
	send(tm, tea.KeyCtrlC)
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}
