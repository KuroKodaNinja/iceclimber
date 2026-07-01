package cli

import (
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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
