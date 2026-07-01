package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
)

// fakeOps records what the console asked it to run.
type fakeOps struct {
	install   *InstallRequest
	bootstrap bool
	reset     bool
	agent     *AgentInstallRequest
}

func (f *fakeOps) RunInstall(r InstallRequest) tea.Cmd {
	f.install = &r // recorded at call time (submitForm now batches the op with a spinner tick)
	return func() tea.Msg { return OpResultMsg{} }
}

func (f *fakeOps) RunBootstrap() tea.Cmd {
	return func() tea.Msg { f.bootstrap = true; return OpResultMsg{} }
}

func (f *fakeOps) RunBootstrapReset() tea.Cmd {
	return func() tea.Msg { f.reset = true; return OpResultMsg{} }
}
func (f *fakeOps) RunAgentInstall(r AgentInstallRequest) tea.Cmd {
	f.agent = &r
	return func() tea.Msg { return OpResultMsg{} }
}
func (f *fakeOps) Agents() []AgentChoice {
	return []AgentChoice{{Name: "claude", DisplayName: "Claude Code"}}
}
func (f *fakeOps) DetectedRuntimes() []RuntimeChoice                   { return nil }
func (f *fakeOps) SetRuntimeSources(map[string]RuntimeSelection) error { return nil }
func (f *fakeOps) PollStatus() tea.Cmd                                 { return func() tea.Msg { return StatusMsg{} } }
func (f *fakeOps) Egress() EgressSnapshot                              { return EgressSnapshot{} }
func (f *fakeOps) ApprovePending(string) error                         { return nil }
func (f *fakeOps) DenyPending(string) error                            { return nil }
func (f *fakeOps) ForgetRule(_, _ string) error                        { return nil }

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

func TestConsole_CounterSeedAndIncrements(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg, 8), "", nil).WithSeedCounts(5, 2, 1)
	if c.served != 5 || c.approved != 2 || c.denied != 1 {
		t.Fatalf("seed: served=%d approved=%d denied=%d, want 5/2/1", c.served, c.approved, c.denied)
	}
	step := func(m tea.Model, kind string) Console {
		u, _ := m.Update(activity.Event{TS: "2026-06-28T18:00:00Z", Kind: kind})
		return u.(Console)
	}
	c = step(c, activity.KindServiced)
	c = step(c, activity.KindApproved) // previously unasserted on the live Console
	c = step(c, activity.KindDenied)   // "
	if c.served != 6 || c.approved != 3 || c.denied != 2 {
		t.Errorf("after one each: served=%d approved=%d denied=%d, want 6/3/2", c.served, c.approved, c.denied)
	}
}

func TestEventToLine_ShowsDuration(t *testing.T) {
	l := eventToLine(activity.Event{Kind: activity.KindServiced, Type: "pip.install", Status: "ok", Detail: "rich", DurMS: 3200})
	if !strings.Contains(l.plain, "3.2s") {
		t.Errorf("serviced line should show total elapsed; got %q", l.plain)
	}
	// No duration recorded → no "·" duration suffix, no dangling spaces.
	l0 := eventToLine(activity.Event{Kind: activity.KindServiced, Type: "ping", Status: "ok"})
	if strings.HasSuffix(l0.plain, " ") || strings.Contains(l0.plain, "· ") {
		t.Errorf("zero-duration line should not show a duration: %q", l0.plain)
	}
}

func TestConsole_ServingIndicatorSetAndClear(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg, 8), "", nil)

	// A started event arms the in-flight indicator — no counter bump, no scrollback line.
	u, _ := c.Update(activity.Event{TS: "2026-06-28T18:00:00Z", Kind: activity.KindStarted, ID: "r1", Type: "pip.install"})
	c = u.(Console)
	if c.serving != "pip.install" || c.servingID != "r1" {
		t.Fatalf("serving=%q id=%q, want pip.install/r1", c.serving, c.servingID)
	}
	if c.served != 0 || len(c.popoLines) != 0 {
		t.Errorf("started must not count or add a scrollback line: served=%d lines=%d", c.served, len(c.popoLines))
	}

	// The matching serviced event clears the indicator and now counts + renders.
	u, _ = c.Update(activity.Event{TS: "2026-06-28T18:00:01Z", Kind: activity.KindServiced, ID: "r1", Type: "pip.install", Status: "ok"})
	c = u.(Console)
	if c.serving != "" {
		t.Errorf("serviced should clear serving, got %q", c.serving)
	}
	if c.served != 1 || len(c.popoLines) != 1 {
		t.Errorf("after serviced: served=%d lines=%d, want 1/1", c.served, len(c.popoLines))
	}
}

// TestConsole_ServingProgressBar: an agent transfer sample (Agent=true) drives the
// serving banner's byte bar (#3) — not the operator footer meter — and is cleared when
// the request completes.
func TestConsole_ServingProgressBar(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg, 8), "", nil)
	u, _ := c.Update(activity.Event{TS: "2026-06-28T18:00:00Z", Kind: activity.KindStarted, ID: "r1", Type: "python.install"})
	c = u.(Console)

	u, _ = c.Update(ProgressMsg{Event: progress.Event{Phase: "transferring", Cur: 5 << 20, Total: 10 << 20, Unit: progress.Bytes}, Transport: "sftp", Agent: true})
	c = u.(Console)
	if c.servingProg == nil {
		t.Fatal("an agent progress sample should set servingProg")
	}
	if c.prog != nil {
		t.Error("agent-request progress must not drive the operator footer meter")
	}
	if out := c.renderServing(); !strings.Contains(out, "50%") || !strings.Contains(out, "via sftp") {
		t.Errorf("serving banner should show the transfer bar; got %q", out)
	}

	// Completion clears the transfer sample with the indicator.
	u, _ = c.Update(activity.Event{TS: "2026-06-28T18:00:01Z", Kind: activity.KindServiced, ID: "r1", Type: "python.install", Status: "ok"})
	c = u.(Console)
	if c.servingProg != nil {
		t.Error("serviced should clear servingProg")
	}
}

// TestConsole_ProgressRoutesByOrigin: progress is routed by ORIGIN, not by what's
// running — an operator action and an agent request can transfer concurrently, so an
// agent sample (Agent=true) must reach the serving banner while an operator sample
// drives the footer meter, regardless of overlap.
func TestConsole_ProgressRoutesByOrigin(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg, 8), "", nil)
	// Arm an agent request (serving) and pretend an operator action is also running.
	u, _ := c.Update(activity.Event{TS: "2026-06-28T18:00:00Z", Kind: activity.KindStarted, ID: "r1", Type: "pip.install"})
	c = u.(Console)
	c.running = "install python" // operator op concurrently in flight

	// Operator sample → footer meter.
	u, _ = c.Update(ProgressMsg{Event: progress.Event{Phase: "downloading", Cur: 1, Total: 2, Unit: progress.Bytes}})
	c = u.(Console)
	// Agent sample → serving banner, even though running != "".
	u, _ = c.Update(ProgressMsg{Event: progress.Event{Phase: "transferring", Cur: 3, Total: 4, Unit: progress.Bytes}, Agent: true})
	c = u.(Console)

	if c.prog == nil || c.prog.Agent {
		t.Errorf("operator sample should drive c.prog; got %+v", c.prog)
	}
	if c.servingProg == nil || !c.servingProg.Agent {
		t.Errorf("agent sample should drive c.servingProg even while running; got %+v", c.servingProg)
	}
}

// TestConsole_SpinnerSingleLoopAndStop: a single tick loop is shared by the operator
// meter and the serving banner (no double-animation), and it stops once both are idle.
func TestConsole_SpinnerSingleLoopAndStop(t *testing.T) {
	c := NewConsole("sbx", make(chan tea.Msg, 8), "", nil)
	// A started request arms serving and starts the spinner loop.
	u, _ := c.Update(activity.Event{TS: "2026-06-28T18:00:00Z", Kind: activity.KindStarted, ID: "r1", Type: "ping"})
	c = u.(Console)
	if !c.spinning {
		t.Fatal("a started request should start the spinner loop")
	}
	// A concurrent operator op must NOT start a second loop.
	if cmd := c.tickSpinner(); cmd != nil {
		t.Error("tickSpinner must be a no-op while already spinning (no double loop)")
	}
	// Completion clears serving; with nothing else in flight, a tick stops the loop.
	u, _ = c.Update(activity.Event{TS: "2026-06-28T18:00:01Z", Kind: activity.KindServiced, ID: "r1", Type: "ping", Status: "ok"})
	c = u.(Console)
	u, cmd := c.Update(spinner.TickMsg{})
	c = u.(Console)
	if c.spinning {
		t.Error("the spinner loop should stop once running and serving are both idle")
	}
	if cmd != nil {
		t.Error("an idle tick should return a nil cmd (loop stopped)")
	}
}

func TestConsole_ServingClearedByGateDeny(t *testing.T) {
	// A gate-denied request produces no serviced event; ClearServingMsg must clear the
	// in-flight indicator so it never sticks on the last request.
	c := NewConsole("sbx", make(chan tea.Msg, 4), "", nil)
	u, _ := c.Update(activity.Event{TS: "2026-06-28T18:00:00Z", Kind: activity.KindStarted, ID: "r9", Type: "python.install"})
	c = u.(Console)
	if c.serving == "" {
		t.Fatal("started should arm serving")
	}
	u, _ = c.Update(ClearServingMsg{})
	c = u.(Console)
	if c.serving != "" || c.servingID != "" {
		t.Errorf("ClearServingMsg should clear serving, got %q/%q", c.serving, c.servingID)
	}
}

func TestEventToLine_StartedStyle(t *testing.T) {
	l := eventToLine(activity.Event{Kind: activity.KindStarted, Type: "pip.install"})
	if !strings.Contains(l.plain, "▸") || !strings.Contains(l.plain, "pip.install") {
		t.Errorf("started line should be a ▸ in-progress form; got %q", l.plain)
	}
}

// TestDashboard_ServingAndRunningCoexist: the agent in-flight banner and the operator
func TestWrapPlain(t *testing.T) {
	// A short line is untouched (alignment preserved).
	if got := wrapPlain("abc def", 40); len(got) != 1 || got[0] != "abc def" {
		t.Errorf("short line wrapped: %q", got)
	}
	// A long line wraps into width-bounded rows that reconstruct the words.
	long := "egress proxy unavailable native-tool egress disabled this session connection refused"
	rows := wrapPlain(long, 20)
	if len(rows) < 2 {
		t.Fatalf("long line should wrap to multiple rows, got %d: %v", len(rows), rows)
	}
	for _, r := range rows {
		if len([]rune(r)) > 20 {
			t.Errorf("wrapped row exceeds width: %q (%d)", r, len([]rune(r)))
		}
	}
	if strings.Join(rows, " ") != long {
		t.Errorf("wrapped rows lost content:\n got %q\nwant %q", strings.Join(rows, " "), long)
	}
	// An over-long single token is hard-broken.
	hb := wrapPlain(strings.Repeat("x", 25), 10)
	if len(hb) != 3 || len([]rune(hb[0])) != 10 {
		t.Errorf("over-long token not hard-broken to width: %v", hb)
	}
}

func TestWindowRows(t *testing.T) {
	rows := []string{"a", "b", "c", "d", "e"}
	if got := windowRows(rows, 2, 0); strings.Join(got, "") != "de" { // follow tail
		t.Errorf("tail window = %v, want [d e]", got)
	}
	if got := windowRows(rows, 2, 1); strings.Join(got, "") != "cd" { // scrolled up one
		t.Errorf("scroll=1 window = %v, want [c d]", got)
	}
	if got := windowRows(rows, 2, 99); strings.Join(got, "") != "ab" { // clamped at top
		t.Errorf("over-scroll window = %v, want [a b] (clamped)", got)
	}
}

func TestPopoPaneRendersLongLineFully(t *testing.T) {
	// A long error line must be fully readable (wrapped), not cut with an ellipsis.
	long := "egress proxy unavailable — native-tool egress disabled this session: ssh: tcpip-forward request denied by peer on port 18080"
	out := popoPane(40, 12, 0, []popoLine{{plain: long, style: dimStyle}})
	if strings.Contains(out, "…") {
		t.Errorf("long line was truncated with an ellipsis; want wrapped:\n%s", out)
	}
	for _, frag := range []string{"tcpip-forward", "denied", "18080"} {
		if !strings.Contains(out, frag) {
			t.Errorf("wrapped pane is missing %q — message not fully readable:\n%s", frag, out)
		}
	}
}

// footer meter are independent — both must render in the same frame (an operator action
// and an agent request can be in flight at once) without clobbering each other.
func TestDashboard_ServingAndRunningCoexist(t *testing.T) {
	out := dashboard(120, 40, "sbx", 0, 0, 0, time.Time{}, ConnConnected, hbStatus{},
		servingLine("⠋", "pip.install", 3*time.Second), // agent in-flight banner
		nil, nil, true, false, true,
		"install python", "⠋ install python · transferring", 0) // operator running + meter + scroll
	if !strings.Contains(out, "▸ servicing pip.install") {
		t.Error("agent serving banner missing from the frame")
	}
	if !strings.Contains(out, "install python") {
		t.Error("operator footer meter missing from the frame")
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
