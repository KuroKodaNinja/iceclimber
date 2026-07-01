package cli

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
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

type fakeTTY struct{ ret string }

func (f fakeTTY) Prompt(string) (string, error) { return f.ret, nil }

func TestTuiPasswordPrompter(t *testing.T) {
	// Not ready (pre-alt-screen): uses the tty fallback, sends nothing on the event channel.
	var ready atomic.Bool
	events := make(chan tea.Msg, 1)
	p := &tuiPasswordPrompter{events: events, done: make(chan struct{}), ready: &ready, tty: fakeTTY{ret: "ttypw"}}
	if got, _ := p.Prompt("Password: "); got != "ttypw" {
		t.Errorf("pre-ready Prompt = %q, want the tty fallback", got)
	}
	if len(events) != 0 {
		t.Error("pre-ready Prompt must not send a modal request")
	}

	// Ready: sends a PasswordRequest to the modal and returns the typed reply.
	ready.Store(true)
	go func() {
		req := (<-events).(*tui.PasswordRequest)
		req.Reply <- "typed-secret"
	}()
	if got, err := p.Prompt("Password: "); err != nil || got != "typed-secret" {
		t.Errorf("ready Prompt = %q, %v; want the modal reply", got, err)
	}

	// Console closed: returns an error rather than blocking forever.
	done := make(chan struct{})
	close(done)
	p2 := &tuiPasswordPrompter{events: make(chan tea.Msg), done: done, ready: &ready, tty: fakeTTY{}}
	if _, err := p2.Prompt("x"); err == nil {
		t.Error("a closed console should make Prompt error, not block")
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

// TestConsoleProgress_NonBlockingDrop: consoleOps.progress must never block the
// install goroutine — a full events channel just drops the sample (the next one
// supersedes it).
func TestConsoleProgress_NonBlockingDrop(t *testing.T) {
	ch := make(chan tea.Msg, 1)
	holder := &sessionHolder{}
	holder.Set(&session{transport: "exec"})
	o := &consoleOps{holder: holder, events: ch}
	fn := o.progress()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ { // far more than the buffer of 1
			fn(progress.Event{Phase: "transferring", Cur: int64(i), Total: 100, Unit: progress.Bytes})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("progress() blocked on a full channel — must drop, not stall the install")
	}
	// The one buffered sample is a transport-tagged ProgressMsg.
	if m, ok := (<-ch).(tui.ProgressMsg); !ok || m.Transport != "exec" {
		t.Errorf("buffered msg = %#v, want a ProgressMsg tagged via exec", m)
	}
}

// TestAwaitBootstrapped_AlreadyProvisioned: a bootstrapped sandbox returns immediately with no
// "press b" event and never blocks — serving proceeds without a park.
func TestAwaitBootstrapped_AlreadyProvisioned(t *testing.T) {
	emitted := 0
	err := awaitBootstrapped(context.Background(),
		func(context.Context) bool { return true }, make(chan struct{}), func(tea.Msg) { emitted++ })
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if emitted != 0 {
		t.Errorf("emitted %d events, want 0 (already bootstrapped ⇒ no park state)", emitted)
	}
}

// TestAwaitBootstrapped_ParksThenResumesOnNudge: an unprovisioned box emits the "press b"
// state and BLOCKS (no spin) until a bootstrapped nudge; once provisioned it returns nil so
// serving starts in place. This is the TUI/cmdline-console analogue of the headless clean-box
// functional test — it guards the acute fix (no reconnect loop, no password-prompt storm).
func TestAwaitBootstrapped_ParksThenResumesOnNudge(t *testing.T) {
	events := make(chan tea.Msg, 8)
	bootstrapped := make(chan struct{}, 1)
	var provisioned atomic.Bool // flips true once the operator bootstraps in place
	done := make(chan error, 1)
	go func() {
		done <- awaitBootstrapped(context.Background(),
			func(context.Context) bool { return provisioned.Load() },
			bootstrapped, func(m tea.Msg) { events <- m })
	}()

	// It must park (emit the "press b" state) and NOT return while unprovisioned.
	select {
	case m := <-events:
		ev, ok := m.(activity.Event)
		if !ok || ev.Type != "bootstrap" || ev.Status != "needs" {
			t.Fatalf("first emit = %+v, want a bootstrap-needs event", m)
		}
		if !strings.Contains(ev.Detail, "press b") {
			t.Errorf("park detail = %q, want it to mention `press b`", ev.Detail)
		}
	case <-time.After(time.Second):
		t.Fatal("awaitBootstrapped should emit the not-bootstrapped state")
	}
	select {
	case <-done:
		t.Fatal("awaitBootstrapped must block while unprovisioned, not return")
	case <-time.After(50 * time.Millisecond):
	}

	// Operator bootstraps in place → provision succeeds, then nudges → serving resumes.
	provisioned.Store(true)
	bootstrapped <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("after bootstrap, err = %v, want nil (resume serving)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a nudge after provisioning should unblock awaitBootstrapped")
	}
}

// TestAwaitBootstrapped_CancelStopsPark: cancelling the context while parked returns ctx.Err()
// so the cycle ends cleanly (caller stops without serving) — never hanging on a clean box.
func TestAwaitBootstrapped_CancelStopsPark(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- awaitBootstrapped(ctx, func(context.Context) bool { return false },
			make(chan struct{}), func(tea.Msg) {})
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel should unblock a parked awaitBootstrapped")
	}
}

// TestNudgeBootstrapped: the nudge is a non-blocking, coalescing signal — one queued at most,
// a no-op when unwired (nil) or already queued (buffer full).
func TestNudgeBootstrapped(t *testing.T) {
	(&consoleOps{}).nudgeBootstrapped() // nil channel → no-op, no panic

	ch := make(chan struct{}, 1)
	o := &consoleOps{bootstrapped: ch}
	o.nudgeBootstrapped()
	o.nudgeBootstrapped() // second is coalesced (buffer full) — must not block
	if len(ch) != 1 {
		t.Fatalf("queued %d nudges, want exactly 1 (coalesced, non-blocking)", len(ch))
	}
}
