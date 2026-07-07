package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/tui"
)

// newTestFleet builds a fleet model over dummy per-sandbox views (nil ops, dummy channel —
// the fleet drives them via Send, so their own event pump is never armed).
func newTestFleet(ids ...string) fleetModel {
	views := make(map[string]tui.Console, len(ids))
	for _, id := range ids {
		views[id] = tui.NewConsole(id, make(chan tea.Msg), "", nil)
	}
	return newFleetModel(ids, views)
}

// fleetWait blocks until ALL substrings have appeared. teatest drains the shared output reader
// across calls, so same-frame substrings must be checked together in one call (not successively).
func fleetWait(t *testing.T, tm *teatest.TestModel, subs ...string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		for _, s := range subs {
			if !bytes.Contains(b, []byte(s)) {
				return false
			}
		}
		return true
	}, teatest.WithDuration(15*time.Second), teatest.WithCheckInterval(15*time.Millisecond))
}

// TestFleet_OverviewAndTabNav: the overview lists every member; heartbeat routes into the row;
// digit/bracket keys switch tabs; 0/esc return to the overview.
func TestFleet_OverviewAndTabNav(t *testing.T) {
	tm := teatest.NewTestModel(t, newTestFleet("alpha", "beta"),
		teatest.WithInitialTermSize(120, 40))

	// Overview shows both members and the tab bar (all one frame → check together).
	fleetWait(t, tm, "SANDBOX", "alpha", "beta", "[overview]")

	// A heartbeat for beta lands in its overview row.
	tm.Send(tui.SandboxMsg{ID: "beta", Msg: tui.HeartbeatMsg{Seq: 7, At: time.Now()}})
	fleetWait(t, tm, "seq 7")

	// Jump to tab 1 (alpha): its console renders and the tab bar marks it active.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	fleetWait(t, tm, "iceclimber ▸ alpha", "[1:alpha]")

	// Back to the overview.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("0")})
	fleetWait(t, tm, "[overview]")

	tm.Quit()
}

// TestFleet_GlobalApproval: an approval from a NON-active box surfaces the global modal without
// switching tabs, badges that box, and answering replies + clears it.
func TestFleet_GlobalApproval(t *testing.T) {
	tm := teatest.NewTestModel(t, newTestFleet("alpha", "beta"),
		teatest.WithInitialTermSize(120, 40))
	fleetWait(t, tm, "[overview]")

	// Sit on alpha's tab, then beta asks for approval.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	fleetWait(t, tm, "iceclimber ▸ alpha")

	reply := make(chan int, 1)
	tm.Send(tui.SandboxMsg{ID: "beta", Msg: &tui.ApprovalRequest{
		Title: "install python", Kind: "operation", Reply: reply,
	}})
	// The global modal shows over alpha's tab (no tab switch needed).
	fleetWait(t, tm, "install python")

	// Approve it.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if got := <-reply; got != tui.Approve {
		t.Fatalf("approval reply = %d, want Approve(%d)", got, tui.Approve)
	}
	tm.Quit()
}

// TestFleet_RoutesActivityToOverview: a serviced event for one box bumps only that box's row.
func TestFleet_RoutesActivityToOverview(t *testing.T) {
	m := newTestFleet("alpha", "beta")
	nm, _ := m.Update(tui.SandboxMsg{ID: "alpha", Msg: activity.Event{
		Kind: activity.KindServiced, TS: time.Now().Format(time.RFC3339),
	}})
	fm := nm.(fleetModel)
	if fm.summ["alpha"].served != 1 {
		t.Errorf("alpha serviced = %d, want 1", fm.summ["alpha"].served)
	}
	if fm.summ["beta"].served != 0 {
		t.Errorf("beta serviced = %d, want 0 (must not cross boxes)", fm.summ["beta"].served)
	}
}

// TestFleet_LockedThenJoinsLive: a member whose serve.lock another process holds renders a ⛔
// row with the holder, then joins live (view swapped, "● serving") when the lock frees (§14.4).
func TestFleet_LockedThenJoinsLive(t *testing.T) {
	m := newTestFleet("alpha", "beta")
	nm, _ := m.Update(memberLockedMsg{id: "beta", holder: " by pid 42 · console · since 10:00:00"})
	fm := nm.(fleetModel)
	if got := fleetLinkState(fm.summ["beta"]); !strings.Contains(got, "⛔") || !strings.Contains(got, "pid 42") {
		t.Fatalf("locked beta link = %q, want ⛔ with the holder", got)
	}

	joined := tui.NewConsole("beta", make(chan tea.Msg), "", nil)
	nm2, _ := fm.Update(memberJoinedMsg{id: "beta", view: joined})
	fm2 := nm2.(fleetModel)
	s := fm2.summ["beta"]
	if s.holder != "" || !s.started {
		t.Errorf("joined beta should be unlocked + started, got %+v", s)
	}
	if got := fleetLinkState(s); got != "● serving" {
		t.Errorf("joined beta link = %q, want ● serving", got)
	}
}

// TestFleet_UnreachableMemberRow: a failed worker start renders "✗ unreachable" and never aborts.
func TestFleet_UnreachableMemberRow(t *testing.T) {
	m := newTestFleet("alpha", "beta")
	nm, _ := m.Update(workerFailedMsg{id: "beta", reason: "dial timeout"})
	fm := nm.(fleetModel)
	if got := fleetLinkState(fm.summ["beta"]); got != "✗ unreachable" {
		t.Errorf("beta link = %q, want ✗ unreachable", got)
	}
	if got := fleetLinkState(fm.summ["alpha"]); got == "✗ unreachable" {
		t.Errorf("alpha should be unaffected, got %q", got)
	}
}
