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
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/python"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
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

// TestConsoleOps_InstallEmitsProgress proves the end-to-end progress wiring over the
// real VM/transport: a runtime install into a FRESH root (so a transfer actually
// happens, not an AlreadyInstalled short-circuit) pushes ProgressMsg samples on the
// events channel, including a "transferring" phase tagged with the active transport.
func TestConsoleOps_InstallEmitsProgress(t *testing.T) {
	path := os.Getenv("ICECLIMBER_CONFIG")
	if path == "" {
		path = filepath.Join("..", "..", "iceclimber.yaml")
	}
	cfg, err := config.Load(path, "")
	if err != nil {
		t.Skipf("no usable config at %s (%v); run `make sandbox-up && make sandbox-config`", path, err)
	}
	cfg.RemoteRoot = fmt.Sprintf("/tmp/iceclimber-prog-%d", time.Now().UnixNano()) // fresh → forces a transfer
	ctx := context.Background()
	sess, err := openSession(ctx, cfg, "auto")
	if err != nil {
		t.Skipf("cannot reach sandbox (%v); run `make sandbox-up`", err)
	}
	defer sess.Close()
	t.Cleanup(func() { _ = sess.fs.RemoveAll(ctx, cfg.RemoteRoot) })
	if err := provision(ctx, sess); err != nil {
		t.Fatalf("provision: %v", err)
	}
	ops, events := newTestOps(t, sess)

	// Runtime-only install (no packages) → resolve/download/transfer/verify.
	if _, ok := ops.RunInstall(tui.InstallRequest{Lang: "python"})().(tui.OpResultMsg); !ok {
		t.Fatal("install did not return OpResultMsg")
	}

	var sawTransfer bool
	for {
		select {
		case m := <-events:
			if pm, ok := m.(tui.ProgressMsg); ok && strings.HasPrefix(pm.Phase, "transferring") {
				sawTransfer = true
				if pm.Transport != sess.transport {
					t.Errorf("ProgressMsg.Transport = %q, want the active transport %q", pm.Transport, sess.transport)
				}
			}
		default:
			if !sawTransfer {
				t.Error("no 'transferring' ProgressMsg emitted during a fresh runtime install")
			}
			return
		}
	}
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

// TestConsoleOps_RuntimeOnly: an install with no packages installs just the runtime
// — the common operator case (the agent installs packages as its code needs them).
// It must operate `python.install` (not pip.install) and Nana echoes the runtime,
// with no package install attempted.
func TestConsoleOps_RuntimeOnly(t *testing.T) {
	sess := consoleSession(t)
	defer sess.Close()
	if err := provision(context.Background(), sess); err != nil {
		t.Fatalf("provision: %v", err)
	}
	ops, events := newTestOps(t, sess)

	evs := runOp(t, ops.RunInstall(tui.InstallRequest{Lang: "python"}), events) // no Pkgs
	if !operatedOK(evs, "python.install") {
		t.Errorf("runtime-only install should operate python.install; events=%+v", evs)
	}
	for _, e := range evs {
		if e.Kind == activity.KindOperated && e.Type == "pip.install" {
			t.Errorf("runtime-only install must not run pip.install; events=%+v", evs)
		}
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

// waitOut blocks until all substrings have rendered in the program output.
func waitOut(t *testing.T, tm *teatest.TestModel, subs ...string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		for _, s := range subs {
			if !bytes.Contains(b, []byte(s)) {
				return false
			}
		}
		return true
	}, teatest.WithDuration(2*time.Minute), teatest.WithCheckInterval(50*time.Millisecond))
}

// TestConsoleTUI_FullInstall is the full-stack TUI test (the analogue of the
// app-building suites): it runs the REAL console program (teatest) wired to a live
// sandbox, drives the install form by keystroke, and asserts that the package
// actually lands in the sandbox AND that Nana's confirmation renders in [NANA].
func TestConsoleTUI_FullInstall(t *testing.T) {
	sess := consoleSession(t)
	defer sess.Close()
	if err := provision(context.Background(), sess); err != nil {
		t.Fatalf("provision: %v", err)
	}

	events := make(chan tea.Msg, 128)
	act := activity.New(filepath.Join(t.TempDir(), "activity.jsonl"))
	ops := &consoleOps{ctx: context.Background(), sess: sess, act: act, events: events}
	model := tui.NewConsole(sess.sandboxID, events, "", ops)
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(120, 40))

	// Drive the install form: i → Python (default) → packages "six" → version blank → submit.
	waitOut(t, tm, "i install")
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	waitOut(t, tm, "language")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // accept Python, advance to packages
	waitOut(t, tm, "requests / figlet")
	tm.Type("six")
	waitOut(t, tm, "six")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // advance to version
	waitOut(t, tm, "3.12 / 24")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // submit ⇒ real install runs in the sandbox

	// The sandbox echo renders in [NANA] once the real install completes (up to 2m).
	waitOut(t, tm, "✓ six", "present")
	tm.Quit()
	tm.WaitFinished(t, teatest.WithFinalTimeout(10*time.Second))

	// Independently confirm the package really is installed in the sandbox.
	bin, err := python.Locate(context.Background(), sess.fs, sess.tree.Root, "3.12", sess.fp.Arch, sess.fp.Libc.Family)
	if err != nil {
		t.Fatalf("python runtime not located after TUI install: %v", err)
	}
	res, err := sess.runner.Run(context.Background(), remote.ShellQuote(bin)+" -m pip show six", nil)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("six not actually installed in the sandbox after the TUI flow (exit %d, err %v)", res.ExitCode, err)
	}
}
