//go:build functional

// Functional validation of the fleet console over a REAL heterogeneous 2-VM fleet (the musl +
// glibc sandboxes). It builds the fleet model with a live per-box console each and drives it by
// keystroke, proving the overview lists both boxes and that every tab is isolated to its own box:
// a status poll in a tab routes THROUGH the fleet (key delegation → that box's ops.PollStatus →
// tagCmd → SandboxMsg) and renders THAT box's real status. Skips unless both boxes are reachable.
//
// Needs both configs: `make sandbox-config` (musl) and `make sandbox-glibc-config` (glibc), with
// both VMs up. Run with: make tui-functional (the glibc box must be up too, or this test skips).
package cli

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/tui"
)

// fleetTestMember dials one sandbox and returns its live console view (real ops), or skips when
// the box's config/reachability is unavailable.
func fleetTestMember(t *testing.T, cfgPath string) (id string, view tui.Console, closeFn func()) {
	t.Helper()
	cfg, err := config.Load(cfgPath, "")
	if err != nil {
		t.Skipf("no config at %s (%v); run `make sandbox-config` && `make sandbox-glibc-config`", cfgPath, err)
	}
	sess, err := openSession(context.Background(), cfg, "auto")
	if err != nil {
		t.Skipf("cannot reach %s (%v); bring both boxes up", cfg.SandboxID, err)
	}
	if err := provision(context.Background(), sess); err != nil {
		sess.Close()
		t.Fatalf("provision %s: %v", cfg.SandboxID, err)
	}
	holder := &sessionHolder{}
	holder.Set(sess)
	ops := &consoleOps{
		ctx: context.Background(), holder: holder,
		act:    activity.New(filepath.Join(t.TempDir(), "activity.jsonl")),
		events: make(chan tea.Msg, 128),
	}
	// A dummy events channel: the fleet drives the view via Send, so its own pump is never armed.
	return cfg.SandboxID, tui.NewConsole(cfg.SandboxID, make(chan tea.Msg), "", ops), func() { sess.Close() }
}

// TestConsoleFleet_TwoBoxes: the overview lists both real boxes, and each tab is isolated to its
// own box — a status poll routes to that box and names it.
func TestConsoleFleet_TwoBoxes(t *testing.T) {
	idA, viewA, closeA := fleetTestMember(t, filepath.Join("..", "..", "iceclimber.yaml"))
	defer closeA()
	idB, viewB, closeB := fleetTestMember(t, filepath.Join("..", "..", "iceclimber-glibc.yaml"))
	defer closeB()
	if idA == idB {
		t.Skip("both configs point at the same sandbox; a fleet test needs two distinct boxes")
	}

	ids := []string{idA, idB}
	sort.Strings(ids)
	views := map[string]tui.Console{idA: viewA, idB: viewB}
	tm := teatest.NewTestModel(t, newFleetModel(ids, views), teatest.WithInitialTermSize(160, 44))

	// The overview lists both real boxes.
	waitOut(t, tm, "SANDBOX", ids[0], ids[1])

	for i, id := range ids {
		tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strconv.Itoa(i + 1))}) // → tab i+1
		waitOut(t, tm, "iceclimber ▸ "+id)                                          // that box's console header
		tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})                 // status poll (real)
		waitOut(t, tm, "Status · sandbox "+id)                                      // routed to THAT box
		tm.Send(tea.KeyMsg{Type: tea.KeyEsc})                                       // close the panel (member handles it)
	}
	tm.Quit()
	tm.WaitFinished(t, teatest.WithFinalTimeout(10*time.Second))
}
