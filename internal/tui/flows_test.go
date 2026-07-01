package tui

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/KuroKodaNinja/iceclimber/internal/progress"

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
	status    StatusSnapshot
	egress    EgressSnapshot
	forgot    chan EgressRule
	approved  chan string
	denied    chan string
	agent     chan AgentInstallRequest
	detected  []RuntimeChoice                  // offered in the bootstrap form
	rtSources chan map[string]RuntimeSelection // captures SetRuntimeSources
}

func newRecordOps() *recordOps {
	return &recordOps{
		install:   make(chan InstallRequest, 1),
		bootstrap: make(chan struct{}, 1),
		forgot:    make(chan EgressRule, 4),
		approved:  make(chan string, 4),
		denied:    make(chan string, 4),
		agent:     make(chan AgentInstallRequest, 1),
		rtSources: make(chan map[string]RuntimeSelection, 1),
	}
}

func (o *recordOps) RunInstall(r InstallRequest) tea.Cmd {
	return func() tea.Msg { o.install <- r; return OpResultMsg{} }
}
func (o *recordOps) RunBootstrap() tea.Cmd {
	return func() tea.Msg { o.bootstrap <- struct{}{}; return OpResultMsg{} }
}
func (o *recordOps) RunAgentInstall(r AgentInstallRequest) tea.Cmd {
	return func() tea.Msg { o.agent <- r; return OpResultMsg{} }
}
func (o *recordOps) Agents() []AgentChoice {
	return []AgentChoice{{Name: "claude", DisplayName: "Claude Code"}}
}
func (o *recordOps) DetectedRuntimes() []RuntimeChoice { return o.detected }
func (o *recordOps) SetRuntimeSources(sel map[string]RuntimeSelection) error {
	o.rtSources <- sel
	return nil
}
func (o *recordOps) PollStatus() tea.Cmd            { return func() tea.Msg { return StatusMsg(o.status) } }
func (o *recordOps) Egress() EgressSnapshot         { return o.egress }
func (o *recordOps) ApprovePending(id string) error { o.approved <- id; return nil }
func (o *recordOps) DenyPending(id string) error    { o.denied <- id; return nil }
func (o *recordOps) ForgetRule(kind, pattern string) error {
	o.forgot <- EgressRule{Kind: kind, Pattern: pattern}
	return nil
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
		// 30s (not 5s) of headroom: under `go test -race ./...` all packages run in
		// parallel and teatest's huh-driven renders can lag badly on a contended CPU — a
		// too-tight wait flakes there even though the flow is correct. This bound is a
		// timeout, not a sleep: a passing flow returns as soon as its substrings render.
	}, teatest.WithDuration(30*time.Second), teatest.WithCheckInterval(15*time.Millisecond))
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
	waitAll(t, tm, "iceclimber ▸ sbx", "[POPO]", "[NANA]", "i install", "a agent", "b bootstrap")
	tm.Quit()
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}

// TestFlow_ConnStateHeader: the LINK indicator follows ConnStateMsg — connected by
// default, "◌ reconnecting…" on a drop, back to connected on reconnect.
func TestFlow_ConnStateHeader(t *testing.T) {
	tm := startConsole(t, newRecordOps())
	waitText(t, tm, "● connected") // default link state, no heartbeat yet

	tm.Send(ConnStateMsg{State: ConnReconnecting})
	waitText(t, tm, "◌ reconnecting…")

	tm.Send(ConnStateMsg{State: ConnConnected})
	waitText(t, tm, "● connected")

	tm.Quit()
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}

// TestFlow_HeartbeatFreshness: a fresh heartbeat shows "serving"; once it goes stale
// (no advance past the threshold) the header flags it — even though the LINK is still
// connected. This is the regression for "green serving while the heartbeat is stale".
func TestFlow_HeartbeatFreshness(t *testing.T) {
	tm := startConsole(t, newRecordOps())
	// A fresh heartbeat → serving.
	tm.Send(HeartbeatMsg{Seq: 7, At: time.Now()})
	waitText(t, tm, "● serving · hb 7")
	// An old heartbeat (older than the stale threshold) with the link still connected
	// → the header must flag it stale, not keep claiming serving.
	tm.Send(HeartbeatMsg{Seq: 7, At: time.Now().Add(-30 * time.Second)})
	waitText(t, tm, "heartbeat stale")
	tm.Quit()
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}

func TestFlow_NanaPaneHint(t *testing.T) {
	tm := startConsole(t, newRecordOps()) // no agent-log
	// With no agent output, the [NANA] pane shows the actionable hint.
	waitText(t, tm, "$ICECLIMBER_HOME/nana")
	tm.Quit()
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}

func TestFlow_NanaPaneTailsAgentLog(t *testing.T) {
	// The console tails the (bridged) agent-log file and renders it in [NANA] — the
	// same path whether the file is the auto-bridged default or an explicit --agent-log.
	logf := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(logf, []byte("nana fetched comic.json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tm := teatest.NewTestModel(t, NewConsole("sbx", make(chan tea.Msg), logf, newRecordOps()),
		teatest.WithInitialTermSize(120, 40))
	waitText(t, tm, "nana fetched comic.json")
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

// TestFlow_ServingIndicator: a started event pins the in-flight banner ("▸ servicing
// <type>") in a real render; the matching serviced event clears it and lands the
// serviced line. The banner is asserted on the live stream; the post-serviced state is
// asserted on the final model + its View (the live spinner floods frames, which makes
// scraping the post-clear frame out of the stream racy — the model/render is correct,
// as TestConsole_ServingIndicatorSetAndClear proves deterministically).
func TestFlow_ServingIndicator(t *testing.T) {
	tm := startConsole(t, nil)
	tm.Send(activity.Event{TS: "2026-06-28T18:00:00Z", Kind: activity.KindStarted, ID: "r1", Type: "pip.install"})
	waitText(t, tm, "▸ servicing pip.install")
	tm.Send(activity.Event{
		TS: "2026-06-28T18:00:01Z", Kind: activity.KindServiced,
		ID: "r1", Type: "pip.install", Status: "ok", Detail: "requests 2.32.3",
	})
	c := finalConsole(t, tm)
	if c.serving != "" {
		t.Errorf("serving should be cleared after serviced, got %q", c.serving)
	}
	if c.served != 1 || len(c.popoLines) != 1 || !strings.Contains(c.popoLines[0].plain, "requests 2.32.3") {
		t.Errorf("serviced should clear the banner and land the line; served=%d lines=%+v", c.served, c.popoLines)
	}
	// serving == "" guarantees View renders no banner (it's gated on c.serving != "").
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

// TestFlow_InstallJava drives the third language through the form: select Java,
// enter a Maven coordinate and a JDK version, submit.
func TestFlow_InstallJava(t *testing.T) {
	ops := newRecordOps()
	tm := startConsole(t, ops)
	waitText(t, tm, "i install")
	press(tm, "i")
	waitText(t, tm, "Java")
	send(tm, tea.KeyDown)  // python → javascript
	send(tm, tea.KeyDown)  // javascript → java
	send(tm, tea.KeyEnter) // accept, advance to packages
	waitText(t, tm, "requests / figlet")
	tm.Type("org.apache.commons:commons-lang3:3.14.0")
	waitText(t, tm, "commons-lang3")
	send(tm, tea.KeyEnter) // advance to version
	waitText(t, tm, "3.12 / 24")
	tm.Type("17")
	waitText(t, tm, "17")
	send(tm, tea.KeyEnter) // submit
	select {
	case r := <-ops.install:
		if r.Lang != "java" {
			t.Fatalf("selecting Java must reach the request; got Lang=%q", r.Lang)
		}
		if r.Pkgs != "org.apache.commons:commons-lang3:3.14.0" || r.Version != "17" {
			t.Fatalf("request = %+v, want {java org.apache.commons:commons-lang3:3.14.0 17}", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("java install never fired")
	}
	finalConsole(t, tm)
}

// TestFlow_InstallRuntimeOnly: packages are optional — the agent decides what to
// install, not the operator. Submitting the form with a blank packages field must
// install just the runtime (empty Pkgs), not block on a validation error.
func TestFlow_InstallRuntimeOnly(t *testing.T) {
	ops := newRecordOps()
	tm := startConsole(t, ops)
	press(tm, "i")
	waitText(t, tm, "language")
	send(tm, tea.KeyEnter) // accept Python (default), advance to packages
	waitText(t, tm, "requests / figlet")
	send(tm, tea.KeyEnter) // packages blank ⇒ advance to version
	waitText(t, tm, "3.12 / 24")
	send(tm, tea.KeyEnter) // version blank ⇒ submit
	select {
	case r := <-ops.install:
		if r.Lang != "python" || r.Pkgs != "" || r.Version != "" {
			t.Fatalf("request = %+v, want {python <blank> <blank>} (runtime-only)", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime-only install never fired")
	}
	finalConsole(t, tm)
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

// TestFlow_AgentInstall drives the console's agent form ('a') with defaults — install
// claude, auth from the environment — and asserts the request reaches the OpRunner.
func TestFlow_AgentInstall(t *testing.T) {
	ops := newRecordOps()
	tm := startConsole(t, ops)
	press(tm, "a")
	waitText(t, tm, "Claude Code") // the agent select (form-specific, not the footer)
	send(tm, tea.KeyEnter)         // accept claude → how
	waitText(t, tm, "relay the agent binary")
	send(tm, tea.KeyEnter) // accept install → binary path
	waitText(t, tm, "binary path")
	send(tm, tea.KeyEnter) // blank bin → auth
	waitText(t, tm, "token in my environment")
	send(tm, tea.KeyEnter) // accept env auth → submit
	select {
	case r := <-ops.agent:
		if r.Name != "claude" || r.Wrap || r.SkipAuth {
			t.Fatalf("request = %+v, want install claude with env auth", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent install never fired")
	}
	finalConsole(t, tm)
}

// TestFlow_AgentWrap drives the form to the wrap path with skip-auth and a pinned
// binary, asserting those choices reach the OpRunner.
func TestFlow_AgentWrap(t *testing.T) {
	ops := newRecordOps()
	tm := startConsole(t, ops)
	press(tm, "a")
	waitText(t, tm, "Claude Code")
	send(tm, tea.KeyEnter)      // claude → how
	waitText(t, tm, "no relay") // the wrap option
	send(tm, tea.KeyDown)       // move to "wrap"
	send(tm, tea.KeyEnter)      // accept wrap → binary path
	waitText(t, tm, "binary path")
	press(tm, "/opt/claude") // type a binary path
	send(tm, tea.KeyEnter)   // → auth
	waitText(t, tm, "configure auth later")
	send(tm, tea.KeyDown)  // move to "skip"
	send(tm, tea.KeyEnter) // submit
	select {
	case r := <-ops.agent:
		if !r.Wrap || r.Bin != "/opt/claude" || !r.SkipAuth {
			t.Fatalf("request = %+v, want wrap /opt/claude skip-auth", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent wrap never fired")
	}
	finalConsole(t, tm)
}

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

// TestFlow_BootstrapRuntimeSource: when a system Python is detected, the bootstrap
// form offers a source choice; selecting "system" persists it (SetRuntimeSources)
// before provisioning.
func TestFlow_BootstrapRuntimeSource(t *testing.T) {
	ops := newRecordOps()
	ops.detected = []RuntimeChoice{{Lang: "python", Version: "3.12.1", Path: "/usr/bin/python3"}}
	tm := startConsole(t, ops)
	press(tm, "b")
	waitText(t, tm, "Python runtime") // the source select (only shown when detected)
	send(tm, tea.KeyDown)             // move managed → system
	send(tm, tea.KeyEnter)            // accept system → confirm field
	waitText(t, tm, "Re-provision this sandbox?")
	press(tm, "y") // confirm
	select {
	case src := <-ops.rtSources:
		if !src["python"].System {
			t.Fatalf("SetRuntimeSources = %+v, want python=system(true)", src)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("choosing a runtime source should persist it")
	}
	select {
	case <-ops.bootstrap:
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap should still provision after the source choice")
	}
	finalConsole(t, tm)
}

// TestFlow_BootstrapCondaEnvManager: when conda is detected, the bootstrap form offers a
// venv/conda env_manager select; choosing conda persists env_manager=conda for a system python.
func TestFlow_BootstrapCondaEnvManager(t *testing.T) {
	ops := newRecordOps()
	ops.detected = []RuntimeChoice{{Lang: "python", Version: "3.12.1", Path: "/usr/bin/python3", EnvManagers: []string{"venv", "conda"}}}
	tm := startConsole(t, ops)
	press(tm, "b")
	waitText(t, tm, "Python runtime")
	send(tm, tea.KeyDown)  // managed → system
	send(tm, tea.KeyEnter) // accept system → env_manager select
	waitText(t, tm, "env_manager")
	send(tm, tea.KeyDown)  // venv → conda
	send(tm, tea.KeyEnter) // accept conda → confirm
	waitText(t, tm, "Re-provision this sandbox?")
	press(tm, "y")
	select {
	case src := <-ops.rtSources:
		if !src["python"].System || src["python"].EnvManager != "conda" {
			t.Fatalf("SetRuntimeSources = %+v, want python=system(conda)", src)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("choosing conda should persist env_manager=conda")
	}
	<-ops.bootstrap
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
func (o *gateOps) RunAgentInstall(AgentInstallRequest) tea.Cmd {
	return func() tea.Msg { <-o.release; return OpResultMsg{} }
}
func (o *gateOps) Agents() []AgentChoice                               { return nil }
func (o *gateOps) DetectedRuntimes() []RuntimeChoice                   { return nil }
func (o *gateOps) SetRuntimeSources(map[string]RuntimeSelection) error { return nil }
func (o *gateOps) PollStatus() tea.Cmd                                 { return nil }
func (o *gateOps) Egress() EgressSnapshot                              { return EgressSnapshot{} }
func (o *gateOps) ApprovePending(string) error                         { return nil }
func (o *gateOps) DenyPending(string) error                            { return nil }
func (o *gateOps) ForgetRule(_, _ string) error                        { return nil }

// TestFlow_InstallProgressMeter: while an install is in flight, ProgressMsg samples
// render in the footer — a byte transfer shows %/transport, a package step shows
// (i/n) — and the meter clears when the op finishes.
func TestFlow_InstallProgressMeter(t *testing.T) {
	ops := &gateOps{release: make(chan struct{})}
	tm := startConsole(t, ops)
	press(tm, "i")
	waitText(t, tm, "language")
	send(tm, tea.KeyEnter)
	waitText(t, tm, "requests / figlet")
	send(tm, tea.KeyEnter) // packages blank → version
	waitText(t, tm, "3.12 / 24")
	send(tm, tea.KeyEnter) // submit ⇒ RunInstall blocks (running stays visible)
	waitText(t, tm, "Python install")

	// A byte-transfer sample → bar/%/transport in the footer.
	tm.Send(ProgressMsg{Event: progress.Event{Phase: "transferring", Cur: 62, Total: 100, Unit: progress.Bytes}, Transport: "exec"})
	waitText(t, tm, "62%")
	waitText(t, tm, "via exec")

	// A package step sample → (i/n).
	tm.Send(ProgressMsg{Event: progress.Event{Phase: "installing six", Cur: 1, Total: 3, Unit: progress.Items}})
	waitText(t, tm, "(1/3)")

	// Finishing the op clears the meter: the idle footer (keybinds) returns and the
	// model drops the progress sample + running label.
	close(ops.release)
	waitText(t, tm, "i install")
	c := finalConsole(t, tm)
	if c.prog != nil || c.running != "" {
		t.Errorf("meter not cleared after finish: prog=%v running=%q", c.prog, c.running)
	}
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
	send(tm, tea.KeyEnter)            // submit ⇒ RunInstall blocks
	waitText(t, tm, "Python install") // the in-flight meter (spinner + label)
	press(tm, "i")                    // input is ignored while running (no second form)
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

// --- Status + egress panels (Phase 2b) --------------------------------------

func TestFlow_StatusPanel(t *testing.T) {
	ops := newRecordOps()
	ops.status = StatusSnapshot{
		Sandbox: "sbx", Heartbeat: "seq 42 · ~3s ago", Queue: "1 awaiting service · 0 awaiting collection",
		Runtimes: []string{"python 3.12.13-aarch64-musl"}, Caps: "Claude Code 1.2.3 · auth ✓ · linux/arm64 (musl)",
	}
	tm := startConsole(t, ops)
	waitText(t, tm, "i install")
	press(tm, "s")
	waitAll(t, tm, "Status", "seq 42", "python 3.12.13", "Claude Code 1.2.3")
	send(tm, tea.KeyEsc) // close
	c := finalConsole(t, tm)
	if c.panel != "" {
		t.Errorf("esc should close the status panel; panel=%q", c.panel)
	}
}

func TestFlow_EgressPanel(t *testing.T) {
	ops := newRecordOps()
	ops.egress = EgressSnapshot{
		Pending: []EgressPending{{ID: "01J", Host: "api.x.com", URL: "https://api.x.com/data"}},
		Rules:   []EgressRule{{Kind: "allow", Pattern: "https://xkcd.com/*"}},
	}
	tm := startConsole(t, ops)
	waitText(t, tm, "i install")
	press(tm, "e")
	waitAll(t, tm, "Egress", "api.x.com", "xkcd.com")

	// Row 0 is the pending entry — approve it.
	press(tm, "a")
	select {
	case id := <-ops.approved:
		if id != "01J" {
			t.Errorf("approved %q, want 01J", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("approve was not called")
	}

	// Move to the rule row and forget it.
	send(tm, tea.KeyDown)
	press(tm, "f")
	select {
	case r := <-ops.forgot:
		if r.Kind != "allow" || r.Pattern != "https://xkcd.com/*" {
			t.Errorf("forgot %+v, want allow https://xkcd.com/*", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("forget was not called")
	}

	send(tm, tea.KeyEsc)
	c := finalConsole(t, tm)
	if c.panel != "" {
		t.Errorf("esc should close the egress panel; panel=%q", c.panel)
	}
}
