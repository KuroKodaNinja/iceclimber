//go:build functional

// Functional validation of the console's operator-action executor (consoleOps)
// against a real Lima/Alpine sandbox — the TUI analogue of the install/scenario
// suites. It drives the same code path the console's forms feed (RunInstall /
// RunBootstrap) and asserts both the controller summary and the sandbox-side echo
// Nana sends back.
//
// Needs a config pointing at a running sandbox: `make sandbox-up && make
// sandbox-config` writes iceclimber.yaml, or set ICECLIMBER_CONFIG. Tests skip
// cleanly when neither is reachable. Run with: make tui-functional.
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/tui"
)

func consoleSession(t *testing.T) *session {
	t.Helper()
	path := os.Getenv("ICECLIMBER_CONFIG")
	if path == "" {
		path = filepath.Join("..", "..", "iceclimber.yaml")
	}
	cfg, err := config.Load(path, "")
	if err != nil {
		t.Skipf("no usable config at %s (%v); run `make sandbox-up && make sandbox-config`", path, err)
	}
	sess, err := openSession(context.Background(), cfg, "auto")
	if err != nil {
		t.Skipf("cannot reach sandbox (%v); run `make sandbox-up`", err)
	}
	return sess
}

func newTestOps(t *testing.T, sess *session) (*consoleOps, chan tea.Msg) {
	t.Helper()
	events := make(chan tea.Msg, 128)
	act := activity.New(filepath.Join(t.TempDir(), "activity.jsonl"))
	return &consoleOps{ctx: context.Background(), sess: sess, act: act, events: events}, events
}

// runOp runs an operator command synchronously and drains the activity events it
// emitted (the [POPO] operated summary + any [NANA] echoes).
func runOp(t *testing.T, cmd tea.Cmd, events chan tea.Msg) []activity.Event {
	t.Helper()
	if _, ok := cmd().(tui.OpResultMsg); !ok {
		t.Fatal("op did not return OpResultMsg")
	}
	var got []activity.Event
	for {
		select {
		case m := <-events:
			if e, ok := m.(activity.Event); ok {
				got = append(got, e)
			}
		default:
			return got
		}
	}
}

func nanaConfirms(evs []activity.Event, sub string) bool {
	for _, e := range evs {
		if e.Side == activity.SideNana && e.Status == "ok" && strings.Contains(e.Detail, sub) {
			return true
		}
	}
	return false
}

func operatedOK(evs []activity.Event, typ string) bool {
	for _, e := range evs {
		if e.Kind == activity.KindOperated && e.Type == typ && e.Status == "ok" {
			return true
		}
	}
	return false
}

// TestConsoleOps_PythonFlow: the console's Python install flow installs the package
// (auto-installing the runtime as needed) and Nana echoes it present.
func TestConsoleOps_PythonFlow(t *testing.T) {
	sess := consoleSession(t)
	defer sess.Close()
	if err := provision(context.Background(), sess); err != nil {
		t.Fatalf("provision: %v", err)
	}
	ops, events := newTestOps(t, sess)

	evs := runOp(t, ops.RunInstall(tui.InstallRequest{Lang: "python", Pkgs: "six"}), events)
	if !operatedOK(evs, "pip.install") {
		t.Errorf("missing ok pip.install; events=%+v", evs)
	}
	if !nanaConfirms(evs, "six") {
		t.Errorf("Nana should confirm six present; events=%+v", evs)
	}
	if !nanaConfirms(evs, "Python") && !nanaConfirms(evs, "python") {
		t.Errorf("Nana should echo the Python runtime version; events=%+v", evs)
	}
}

// TestConsoleOps_JavaScriptFlow: the JavaScript flow installs an npm package
// (auto-installing Node) and Nana echoes it present.
func TestConsoleOps_JavaScriptFlow(t *testing.T) {
	sess := consoleSession(t)
	defer sess.Close()
	if err := provision(context.Background(), sess); err != nil {
		t.Fatalf("provision: %v", err)
	}
	ops, events := newTestOps(t, sess)

	evs := runOp(t, ops.RunInstall(tui.InstallRequest{Lang: "javascript", Pkgs: "left-pad"}), events)
	if !operatedOK(evs, "npm.install") {
		t.Errorf("missing ok npm.install; events=%+v", evs)
	}
	if !nanaConfirms(evs, "left-pad") {
		t.Errorf("Nana should confirm left-pad present; events=%+v", evs)
	}
}

// TestConsoleOps_BootstrapFlow: the bootstrap action provisions the tree and Nana
// echoes the ping/pong round-trip.
func TestConsoleOps_BootstrapFlow(t *testing.T) {
	sess := consoleSession(t)
	defer sess.Close()
	ops, events := newTestOps(t, sess)

	evs := runOp(t, ops.RunBootstrap(), events)
	if !operatedOK(evs, "bootstrap") {
		t.Errorf("missing ok bootstrap; events=%+v", evs)
	}
	if !nanaConfirms(evs, "pong") {
		t.Errorf("Nana should echo the sandbox pong; events=%+v", evs)
	}
}
