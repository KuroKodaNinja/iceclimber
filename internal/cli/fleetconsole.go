package cli

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/tui"
)

// The fleet console (§14.3): one Bubble Tea program driving EVERY member. Tab 0 is a live
// overview (one row per box); tabs 1..N are the ordinary single-sandbox tui.Console per box
// (unchanged, per #91). Approvals are GLOBAL — any member's approver feeds one modal queue
// answered without switching tabs. This file is the tea.Model + routing; runFleetConsole wires
// the workers to it (worker events arrive via program.Send tagged with the sandbox ID).

// memberSummary is the overview's per-box row state, updated live as routed messages flow.
type memberSummary struct {
	conn    tui.ConnState // link state once serving
	started bool          // a worker reported ready at least once
	failed  string        // start/dial failure → "✗ unreachable" (reason); "" = fine
	hbSeq   string
	hbAt    time.Time
	served  int
	pending int       // queued approvals originating from this box
	last    time.Time // last serviced/approved/denied action
}

// fleetTickMsg drives a periodic re-render so time-relative fields (heartbeat age, "last
// action") stay fresh even when no message arrives.
type fleetTickMsg time.Time

func fleetTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return fleetTickMsg(t) })
}

// workerReadyMsg / workerFailedMsg report async worker start outcomes (§14.3: a member whose
// dial fails renders unreachable and never aborts the fleet).
type workerReadyMsg struct{ id string }
type workerFailedMsg struct {
	id     string
	reason string
}

// fleetModel is the top tea.Model. Member views are held by value (tui.Console has value-receiver
// Update, Bubble Tea copy semantics); form state lives behind a pointer inside Console, so a
// map of values is safe.
type fleetModel struct {
	ids     []string               // sorted member order → tabs 1..len(ids)
	views   map[string]tui.Console // per-sandbox view (the tab body)
	summ    map[string]*memberSummary
	active  int                    // 0 = overview; 1..len(ids) = that member's tab
	modals  []*tui.ApprovalRequest // GLOBAL approval FIFO (head shown, rest badged)
	pwReqs  []*tui.PasswordRequest // GLOBAL password FIFO
	pwInput string
	w, h    int
	quit    bool
}

// newFleetModel builds the fleet console over pre-built per-sandbox views (ids in display
// order). Split from runFleetConsole so it is teatest-able with dummy views + tm.Send, exactly
// like the single-sandbox console's flows_test.
func newFleetModel(ids []string, views map[string]tui.Console) fleetModel {
	summ := make(map[string]*memberSummary, len(ids))
	for _, id := range ids {
		summ[id] = &memberSummary{}
	}
	return fleetModel{ids: ids, views: views, summ: summ, w: 100, h: 30}
}

// runFleetConsole serves EVERY target in one TUI (§14.3). Members start sequentially BEFORE the
// alt-screen — so each box's first-contact host-key trust prompt and any password prompt run on
// the tty one at a time (startSandboxWorker's own inline flow), never corrupting the fleet
// screen. A member that can't lock or dial becomes an "✗ unreachable" row and never aborts the
// others (§14.7). Then one Bubble Tea program runs the fleet model, with a drain goroutine per
// worker forwarding its (sandbox-tagged) events via program.Send.
//
// Deferred to a later slice (§14.9 discipline): async "connecting…" rows that fill in live
// (today start blocks on a slow dial, bounded by SSH's own connect timeout), and lock-retry ⛔
// rows (a locked member is skipped with a notice rather than shown + periodically re-acquired).
func runFleetConsole(parent context.Context, targets []*config.Config, transport, agentLog string) error {
	ids := make([]string, 0, len(targets))
	for _, cfg := range targets {
		ids = append(ids, cfg.SandboxID)
	}
	sort.Strings(ids)
	byID := map[string]*config.Config{}
	for _, cfg := range targets {
		byID[cfg.SandboxID] = cfg
	}

	views := make(map[string]tui.Console, len(ids))
	workers := map[string]*sandboxWorker{}
	locks := map[string]*sandboxLock{}
	unreachable := map[string]string{} // id → reason (rendered as ✗ unreachable)

	for _, id := range ids {
		cfg := byID[id]
		lk, err := acquireServeLock(cfg, "console")
		if err != nil {
			unreachable[id] = "locked: " + err.Error()
			continue
		}
		// startSandboxWorker dials + (if needed) prompts host-key trust / password on the tty —
		// fine here, we are not on the alt-screen yet. A failure leaves the member unreachable.
		w, err := startSandboxWorker(parent, cfg, transport, agentLog)
		if err != nil {
			lk.Release()
			unreachable[id] = err.Error()
			continue
		}
		locks[id] = lk
		workers[id] = w
		// A dummy channel: in fleet mode the worker's real events are drained by this program via
		// Send, so the view's own event pump must never be armed (the fleet never calls its Init).
		views[id] = tui.NewConsole(id, make(chan tea.Msg), w.agentLogPath, w.ops).
			WithSeedCounts(w.seed.Served, w.seed.Approved, w.seed.Denied, w.seed.LastAction).
			WithEgressLog(auditPath(cfg))
	}
	defer func() {
		for _, w := range workers {
			w.stop()
		}
		for _, lk := range locks {
			lk.Release()
		}
	}()

	// Reachable-with-no-view members (locked/failed) still get a placeholder view so their tab
	// exists; the overview shows them as unreachable.
	for id, reason := range unreachable {
		views[id] = tui.NewConsole(id, make(chan tea.Msg), "", nil)
		_ = reason
	}

	model := newFleetModel(ids, views)
	for id := range workers {
		if s := model.summ[id]; s != nil {
			s.started, s.conn = true, tui.ConnConnected
		}
	}
	for id, reason := range unreachable {
		if s := model.summ[id]; s != nil {
			s.failed = reason
		}
	}
	if len(workers) == 0 {
		return fmt.Errorf("no fleet member could be served (all locked or unreachable)")
	}

	prog := tea.NewProgram(model, tea.WithAltScreen())
	done := make(chan struct{})
	for id, w := range workers {
		go func(id string, ch <-chan tea.Msg) {
			for {
				select {
				case <-done:
					return
				case msg := <-ch:
					prog.Send(tagFleet(id, msg))
				}
			}
		}(id, w.events)
	}
	_, err := prog.Run()
	close(done)
	return err
}

func (m fleetModel) Init() tea.Cmd { return fleetTick() }

func (m fleetModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		// Members render below the one-line tab bar, so give them h-1.
		sub := tea.WindowSizeMsg{Width: msg.Width, Height: msg.Height - 1}
		for id, v := range m.views {
			nv, _ := v.Update(sub)
			m.views[id] = nv.(tui.Console)
		}
		return m, nil
	case fleetTickMsg:
		return m, fleetTick()
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tui.SandboxMsg:
		return m.route(msg.ID, msg.Msg)
	case workerReadyMsg:
		if s := m.summ[msg.id]; s != nil {
			s.started, s.failed, s.conn = true, "", tui.ConnConnected
		}
		return m, nil
	case workerFailedMsg:
		if s := m.summ[msg.id]; s != nil {
			s.failed = msg.reason
		}
		return m, nil
	}
	return m, nil
}

// route dispatches a member's message: approvals/passwords go to the GLOBAL queue (not the
// member view); overview-relevant messages update the member's summary AND forward to its view;
// everything else forwards to the view, with the view's returned cmd re-tagged so its result
// comes back to the same member.
func (m fleetModel) route(id string, msg tea.Msg) (tea.Model, tea.Cmd) {
	s := m.summ[id]
	switch v := msg.(type) {
	case *tui.ApprovalRequest:
		if v.Sandbox == "" {
			v.Sandbox = id
		}
		m.modals = append(m.modals, v)
		if s != nil {
			s.pending++
		}
		return m, nil // GLOBAL — the member view never sees it in fleet mode
	case *tui.PasswordRequest:
		if v.Sandbox == "" {
			v.Sandbox = id
		}
		if len(m.pwReqs) == 0 {
			m.pwInput = ""
		}
		m.pwReqs = append(m.pwReqs, v)
		return m, nil
	case tui.ConnStateMsg:
		if s != nil {
			s.conn = v.State
		}
	case tui.HeartbeatMsg:
		if s != nil {
			s.hbSeq, s.hbAt = strconv.FormatInt(v.Seq, 10), v.At
		}
	case activity.Event:
		if s != nil {
			applyActivitySummary(s, v)
		}
	}
	if vw, ok := m.views[id]; ok {
		nv, cmd := vw.Update(msg)
		m.views[id] = nv.(tui.Console)
		return m, tagCmd(id, cmd)
	}
	return m, nil
}

// applyActivitySummary mirrors Console.applyEvent's counter rules for the overview row: a
// serviced/approved/denied event advances "last action"; serviced also bumps the count.
func applyActivitySummary(s *memberSummary, e activity.Event) {
	if e.Side == activity.SideNana {
		return
	}
	switch e.Kind {
	case activity.KindServiced:
		s.served++
		s.last = parseTS(e.TS)
	case activity.KindApproved, activity.KindDenied:
		s.last = parseTS(e.TS)
	}
}

func parseTS(ts string) time.Time {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t
	}
	return time.Time{}
}

// handleKey applies key precedence: a global password/approval modal first, then fleet
// navigation, then — if a member tab is active and not busy with its own form/panel —
// delegation to that member view.
func (m fleetModel) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.Type == tea.KeyCtrlC {
		m.quit = true
		return m, tea.Quit
	}
	// 1. Global password modal owns the keyboard.
	if len(m.pwReqs) > 0 {
		return m.handlePasswordKey(k)
	}
	// 2. Global approval modal.
	if len(m.modals) > 0 {
		if ch, ok := tui.ModalKey(k.String()); ok {
			req := m.modals[0]
			m.modals = m.modals[1:]
			if s := m.summ[req.Sandbox]; s != nil && s.pending > 0 {
				s.pending--
			}
			req.Reply <- ch
		}
		return m, nil
	}
	// 3. A busy member (open form/panel) keeps its keys.
	if id := m.activeID(); id != "" && m.views[id].Busy() {
		return m.delegate(id, k)
	}
	// 4. Fleet navigation.
	switch s := k.String(); s {
	case "q":
		m.quit = true
		return m, tea.Quit
	case "0":
		m.active = 0
		return m, nil
	case "[":
		m.cycle(-1)
		return m, nil
	case "]":
		m.cycle(1)
		return m, nil
	case "esc":
		m.active = 0 // back to overview
		return m, nil
	default:
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(m.ids) {
			m.active = n
			return m, nil
		}
	}
	// 5. Otherwise delegate to the active member (letter keys: status/egress/install/…).
	if id := m.activeID(); id != "" {
		return m.delegate(id, k)
	}
	return m, nil
}

func (m fleetModel) handlePasswordKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEnter:
		req := m.pwReqs[0]
		m.pwReqs = m.pwReqs[1:]
		req.Reply <- m.pwInput
		m.pwInput = ""
		return m, nil
	case tea.KeyEsc:
		req := m.pwReqs[0]
		m.pwReqs = m.pwReqs[1:]
		req.Reply <- "" // cancel
		m.pwInput = ""
		return m, nil
	case tea.KeyBackspace:
		if r := []rune(m.pwInput); len(r) > 0 {
			m.pwInput = string(r[:len(r)-1])
		}
		return m, nil
	case tea.KeyRunes, tea.KeySpace:
		m.pwInput += string(k.Runes)
		return m, nil
	}
	return m, nil
}

// delegate forwards a key to a member view, re-tagging its cmd so the result returns to it.
func (m fleetModel) delegate(id string, k tea.KeyMsg) (tea.Model, tea.Cmd) {
	nv, cmd := m.views[id].Update(k)
	m.views[id] = nv.(tui.Console)
	return m, tagCmd(id, cmd)
}

func (m *fleetModel) cycle(d int) {
	if len(m.ids) == 0 {
		return
	}
	// active 0 (overview) + cycle → first/last tab; wrap within 1..N.
	m.active += d
	if m.active < 1 {
		m.active = len(m.ids)
	} else if m.active > len(m.ids) {
		m.active = 1
	}
}

// activeID is the sandbox id of the active member tab, or "" when the overview is showing.
func (m fleetModel) activeID() string {
	if m.active >= 1 && m.active <= len(m.ids) {
		return m.ids[m.active-1]
	}
	return ""
}

// tagCmd wraps a member view's returned cmd so its result routes back to that member (spinner
// ticks, poll results, …). A nil cmd stays nil.
func tagCmd(id string, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		if msg == nil {
			return nil
		}
		return tui.SandboxMsg{ID: id, Msg: msg}
	}
}

// tagFleet normalizes a worker's emission to exactly one SandboxMsg layer carrying the worker's
// id (the worker already wraps some of its own messages; collapse the double layer).
func tagFleet(id string, msg tea.Msg) tea.Msg {
	if sm, ok := msg.(tui.SandboxMsg); ok {
		return tui.SandboxMsg{ID: id, Msg: sm.Msg}
	}
	return tui.SandboxMsg{ID: id, Msg: msg}
}

func (m fleetModel) View() string {
	if m.quit {
		return ""
	}
	if len(m.pwReqs) > 0 {
		return tui.PasswordView(m.w, m.h, m.pwLabel(), len([]rune(m.pwInput)))
	}
	if len(m.modals) > 0 {
		return tui.ModalView(m.w, m.h, m.modals[0], len(m.modals)-1)
	}
	body := ""
	if id := m.activeID(); id != "" {
		body = m.views[id].View()
	} else {
		body = m.overview()
	}
	return m.tabBar() + "\n" + body
}

// pwLabel prefixes the password prompt with its sandbox so the operator knows which box asks.
func (m fleetModel) pwLabel() string {
	req := m.pwReqs[0]
	if req.Sandbox != "" {
		return "[" + req.Sandbox + "] " + req.Label
	}
	return req.Label
}

// tabBar renders the one-line tab strip: overview + one entry per box (active bracketed, a
// pending-approvals count badged).
func (m fleetModel) tabBar() string {
	parts := []string{tabLabel("overview", m.active == 0, 0)}
	for i, id := range m.ids {
		pending := 0
		if s := m.summ[id]; s != nil {
			pending = s.pending
		}
		parts = append(parts, tabLabel(fmt.Sprintf("%d:%s", i+1, id), m.active == i+1, pending))
	}
	return strings.Join(parts, "  ")
}

func tabLabel(name string, active bool, pending int) string {
	if pending > 0 {
		name = fmt.Sprintf("%s(%d)", name, pending)
	}
	if active {
		return "[" + name + "]"
	}
	return " " + name + " "
}

// overview renders one row per member: link state, heartbeat, serviced count, pending badge,
// last action — the fleet's health at a glance (§14.3).
func (m fleetModel) overview() string {
	var b strings.Builder
	const f = "%-16s %-16s %-22s %-9s %-9s %s\n"
	fmt.Fprintf(&b, f, "SANDBOX", "LINK", "HEARTBEAT", "SERVICED", "PENDING", "LAST ACTION")
	for _, id := range m.ids {
		s := m.summ[id]
		if s == nil {
			s = &memberSummary{}
		}
		hb := "—"
		if s.hbSeq != "" {
			hb = "seq " + s.hbSeq
			if !s.hbAt.IsZero() {
				hb += fmt.Sprintf(" (~%s)", ageStr(time.Since(s.hbAt)))
			}
		}
		pending := "—"
		if s.pending > 0 {
			pending = strconv.Itoa(s.pending)
		}
		last := "—"
		if !s.last.IsZero() {
			last = ageStr(time.Since(s.last)) + " ago"
		}
		fmt.Fprintf(&b, f, id, fleetLinkState(s), hb, strconv.Itoa(s.served), pending, last)
	}
	return b.String()
}

// fleetLinkState maps a member's summary to its overview link cell.
func fleetLinkState(s *memberSummary) string {
	switch {
	case s.failed != "":
		return "✗ unreachable"
	case !s.started:
		return "· connecting"
	case s.conn == tui.ConnReconnecting:
		return "◌ reconnecting"
	default:
		return "● serving"
	}
}

// ageStr renders a short duration like "3s" / "2m" / "1h".
func ageStr(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	default:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
}
