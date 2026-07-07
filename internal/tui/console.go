package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/audit"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/tail"
)

// ProgressMsg is one live install-progress sample pushed onto the console's event
// channel by the executor (with the active transport label). Operator-initiated
// samples drive the footer meter; agent-initiated ones (Agent=true) drive the
// in-flight serving banner — routed by origin, not by what's running, since an
// operator action and an agent request can transfer concurrently.
type ProgressMsg struct {
	progress.Event
	Transport string // "sftp" | "exec" — how the transfer is happening
	Agent     bool   // true = a Nana-issued request's transfer (serving banner, not footer)
}

// required is a huh validator that rejects blank/whitespace input.
func required(what string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New("enter " + what)
		}
		return nil
	}
}

// Operator choices returned on an ApprovalRequest.Reply.
const (
	Approve    = iota // allow this once
	ApproveAll        // allow + remember (per host / per verb-type)
	Deny              // deny this once
	DenyAll           // deny + remember
)

// ApprovalRequest is a held operation shown as a modal. The dispatcher (via the
// console's asker) sends it on the event channel and blocks; the operator's choice
// (one of the constants above) is sent back on Reply. The fields are
// presentation-neutral so the cli layer can fill them from its own prompt type.
type ApprovalRequest struct {
	Sandbox       string
	Title         string
	Kind          string // "operation" | "egress"
	Fields        [][2]string
	Note          string
	RememberLabel string
	Reply         chan int
}

// PasswordRequest is a no-echo secret prompt (an SSH password / kbd-interactive challenge
// during a reconnect) shown as a masked modal, so the prompt never writes to /dev/tty and
// corrupts the alt-screen. The cli's TUI-backed prompter sends it on the event channel and
// blocks; the typed value is sent back on Reply (empty string if the operator cancels).
type PasswordRequest struct {
	Sandbox string // which box is asking (labels the modal in the fleet console; "" single-box)
	Label   string
	Reply   chan string
}

// InstallRequest is an operator-initiated install (not an agent maildir request),
// filled from the console's install form and handed to the OpRunner. The operator
// picks a language (Python / JavaScript / Java / Rust) and the packages to install; the
// cli layer ensures the runtime exists (installing it at Version, or the recommended
// default when blank) and derives the package manager (pip / npm) and resolution tier.
// When Project is set the cli layer instead builds that sandbox project (Maven / npm /
// Cargo / conda env) and the Pkgs field is ignored.
type InstallRequest struct {
	Lang    string // "python" | "javascript" | "java" | "rust" | "shell" | "tool"
	Version string // runtime version to ensure/target (blank = recommended default)
	Pkgs    string // space/comma-separated package specs (ignored when Project is set)
	Project string // build this sandbox project dir instead of installing packages (pom.xml/package.json/Cargo.toml/environment.yml)
	Tool    string // lang=="tool": the catalog tool name to install
}

// ToolChoice is one catalog utility offered in the console's tool picker.
type ToolChoice struct{ Name, Version, Description string }

// AgentInstallRequest is an operator request to install/wrap a coding agent (the
// nana wrapper). Wrap uses a binary already on the sandbox (no relay); Bin optionally
// pins its path. SkipAuth installs without configuring a token (else the token is
// read from the operator's environment).
type AgentInstallRequest struct {
	Name     string // agent name, e.g. "claude"
	Wrap     bool   // wrap an existing binary instead of relaying one in
	Bin      string // wrap only: explicit absolute path (blank = found on PATH)
	SkipAuth bool
}

// AgentChoice is one installable agent offered in the console's agent form.
type AgentChoice struct{ Name, DisplayName string }

// RuntimeChoice is a system runtime the console offers as a bootstrap source option
// (the operator can use it instead of an iceclimber-managed runtime). EnvManagers lists
// the isolation tools the box supports for it (python: "venv","conda").
type RuntimeChoice struct {
	Lang, Version, Path string
	EnvManagers         []string
}

// RuntimeSelection is the operator's bootstrap choice for one runtime: use the system
// runtime (else managed), and — for a system python — which env_manager to isolate with
// ("" = the default venv, or "conda").
type RuntimeSelection struct {
	System     bool
	EnvManager string
}

// OpResultMsg signals an operator-initiated action finished; it clears the running
// indicator. The pane line is driven separately by the activity event the runner
// emits, so the result reads identically whether it was operator- or agent-driven.
type OpResultMsg struct{}

// OpRunner executes operator-initiated actions. Each method returns a tea.Cmd that
// performs the work off the UI goroutine, appends an activity event (so the [POPO]
// pane and JSONL both reflect it), and finally emits an OpResultMsg. The cli layer
// supplies it (it holds the session); a nil OpRunner disables the management menu.
type OpRunner interface {
	RunInstall(InstallRequest) tea.Cmd
	RunBootstrap() tea.Cmd
	// RunBootstrapReset destructively wipes the sandbox (runtimes + state) then reprovisions.
	RunBootstrapReset() tea.Cmd
	// RunAgentInstall installs/wraps a coding agent (the nana wrapper).
	RunAgentInstall(AgentInstallRequest) tea.Cmd
	// Agents lists the installable agents (for the agent form's picker). Local.
	Agents() []AgentChoice
	// DetectedRuntimes lists system runtimes the operator may opt into at bootstrap. Local.
	DetectedRuntimes() []RuntimeChoice
	// CatalogTools lists the installable catalog utilities (for the install form's tool
	// picker). Local (no SSH).
	CatalogTools() []ToolChoice
	// SetRuntimeSources persists the operator's per-language bootstrap choice (system vs
	// managed, and the env_manager for a system python). Local.
	SetRuntimeSources(sel map[string]RuntimeSelection) error
	// PollStatus returns a cmd that reads sandbox status (SSH) and emits a StatusMsg.
	PollStatus() tea.Cmd
	// Egress reads the operator's persisted rules + pending held requests (local).
	Egress() EgressSnapshot
	// ApprovePending / DenyPending resolve a held request (add a host allow/deny rule
	// and drop it from pending); ForgetRule removes a persisted rule. All local.
	ApprovePending(id string) error
	DenyPending(id string) error
	ForgetRule(kind, pattern string) error
}

// StatusSnapshot is the sandbox status shown in the console's status panel.
type StatusSnapshot struct {
	Sandbox   string
	Heartbeat string // "seq 42 · ~3s ago" or "none yet"
	Queue     string // "1 awaiting service · 0 awaiting collection"
	Runtimes  []string
	Caps      string // "" if the agent hasn't reported
	Err       string // set when the sandbox is unreachable (SSH dropped); panel shows it
}

// StatusMsg delivers a fresh StatusSnapshot to the console.
type StatusMsg StatusSnapshot

// ConnState is the console's SSH connection state, driving the header indicator.
type ConnState int

const (
	ConnConnected    ConnState = iota // serving over a live connection (console default)
	ConnReconnecting                  // the link dropped; the supervisor is reconnecting
	ConnViewing                       // passive log viewer (`iceclimber tui`); not connected
)

// ConnStateMsg updates the header's connection indicator. The serve supervisor emits
// it on (re)connect and on a drop, so the header reflects reality instead of always
// claiming "serving".
type ConnStateMsg struct{ State ConnState }

// SandboxMsg tags a UI-bound message with the sandbox it came from. Every sandboxWorker
// wraps its emissions in one, which is what makes a FLEET console possible: N workers
// fan into one program and the shell routes each message to its sandbox's view by ID.
// The single-sandbox Console simply unwraps (the ID is implicit). Untagged messages
// remain valid (tests, ops-layer sends) — both paths are handled.
type SandboxMsg struct {
	ID  string
	Msg tea.Msg
}

// ClearServingMsg clears the in-progress "serving" indicator for one request without a
// serviced event — used when a request is denied by the gate (which produces no serviced event
// and whose denial is logged durably, not pushed live), so the indicator never sticks. ID names
// the request to clear (matching its KindStarted); an empty ID clears all in-flight (defensive).
type ClearServingMsg struct{ ID string }

// servingReq is one in-flight request tracked by the console's serving indicator.
type servingReq struct {
	typ   string
	start time.Time
}

// HeartbeatMsg reports a heartbeat write (the dispatcher's OnHeartbeat). The header
// uses the arrival time to show serving-vs-stale independent of the SSH link state —
// so a connected-but-wedged dispatcher (no heartbeats advancing) reads as stale.
type HeartbeatMsg struct {
	Seq int64
	At  time.Time
}

// heartbeatStale is how long without an advancing heartbeat before the header flags it
// (≈3× the 2s serve interval, with slack to avoid false positives).
const heartbeatStale = 8 * time.Second

// EgressRule is one persisted allow/deny rule; EgressPending is one held request.
type EgressRule struct{ Kind, Pattern string } // Kind: "allow" | "deny"
type EgressPending struct{ ID, Host, URL string }

// EgressSnapshot is the operator's egress state (pending first, then rules). Recent is
// the tail of the egress audit trail (pre-rendered display lines), shown read-only below
// the rules so the TUI's egress home mirrors `iceclimber logs`' [EGRESS] feed.
type EgressSnapshot struct {
	Pending []EgressPending
	Rules   []EgressRule
	Recent  []string
}

// rows counts only the selectable rows (pending + rules); Recent is read-only display.
func (e EgressSnapshot) rows() int { return len(e.Pending) + len(e.Rules) }

// Console is the embed-serve operator console: it renders the live activity feed
// ([POPO]) and the agent stream ([NANA]), surfaces approval modals fed from the
// dispatcher, and (when an OpRunner is supplied) drives operator-initiated installs
// and bootstrap via huh forms. It is fed by an event channel carrying
// activity.Event (serviced requests) and *ApprovalRequest (held operations).
type Console struct {
	sandboxID string
	events    <-chan tea.Msg
	nana      *tail.Reader
	auditTail *tail.Reader // audit-log tail for live [EGRESS] lines in the [POPO] pane (nil = off)
	ops       OpRunner
	popoLines []popoLine
	nanaLines []string
	served    int
	approved  int
	denied    int
	lastTS    time.Time
	// modals is the FIFO of held operations awaiting the operator (head is displayed;
	// the rest wait, badged). A queue — not a single pointer — because at fleet scale N
	// sandboxes' approvers can each have one outstanding ask; overwriting would orphan a
	// Reply channel and deadlock that sandbox's dispatcher worker.
	modals    []*ApprovalRequest
	pwReqs    []*PasswordRequest // FIFO of no-echo password modals (same reasoning)
	pwInput   string             // typed secret so far, for the head request (rendered masked)
	form      *huh.Form
	formKind  string // "install" | "bootstrap" while a form is open
	running   string // label of an in-flight operator action ("" = idle)
	spin      spinner.Model
	spinning  bool         // a spinner tick loop is live (so running + serving share ONE loop)
	prog      *ProgressMsg // latest progress sample for the in-flight action ("" running = ignored)
	progStart time.Time    // when the current phase began (for ETA)
	// inflight tracks every request the dispatcher is currently servicing, keyed by request ID
	// — distinct from operator `running` so agent-driven requests and an operator action can
	// show in-flight at once. A KindStarted adds an entry, the matching serviced/denied (or a
	// ClearServingMsg) removes it. ID-keyed because concurrent dispatch services several at once
	// (the bounded worker pool); the banner shows a detailed meter for a single request and a
	// "▸ servicing N: <types>" summary for several.
	inflight    map[string]servingReq
	servingProg *ProgressMsg // latest agent transfer sample; shown in the meter only when exactly one is in flight
	panel       string       // "" | "status" | "egress" — an open read/manage panel
	status      *StatusSnapshot
	egress      EgressSnapshot
	cursor      int    // selected row in the egress panel
	panelErr    string // last egress action error, shown in the panel ("" = none)
	width       int
	height      int
	scroll      int       // pane scrollback offset in wrapped rows (0 = follow the newest)
	connState   ConnState // SSH link state (connected vs reconnecting)
	// Heartbeat freshness — distinct from the link: a connected-but-wedged dispatcher
	// stops advancing the seq, which the header surfaces as "stale".
	lastHeartbeat time.Time
	heartbeatSeq  int64

	// form-bound values. Held behind a pointer so the huh form and every
	// (value-copied) Console share one struct — binding to &c.field directly
	// would dangle to a stale copy once Bubble Tea stores the returned model.
	st *formState
}

// formState holds the operator form's bound values.
type formState struct {
	lang    string
	version string
	pkgs    string
	confirm bool
	// agent form
	agentName string
	agentMode string // "install" | "wrap"
	agentBin  string
	agentAuth string // "env" | "skip"
	// bootstrap form: per-detected-runtime source choice, lang → *("managed"|"system"). One
	// entry per runtime the box offers; huh binds each Select to its pointer.
	rtSource map[string]*string
	// bootstrap form: env_manager for a system python ("venv"|"conda"), offered only when
	// the box has conda; ignored unless python's source is "system".
	pyEnvManager string
	// bootstrap form: "provision" (safe, default) | "reset" (destructive — wipe runtimes + state).
	bootstrapAction string
	tool            string // install form: selected catalog tool (lang=="tool")
	project         string // install form: build this project dir instead of installing packages
}

// NewConsole builds a console reading events (and, optionally, the agent stream).
// ops may be nil to disable the operator management menu (e.g. in tests).
func NewConsole(sandboxID string, events <-chan tea.Msg, agentLog string, ops OpRunner) Console {
	c := Console{sandboxID: sandboxID, events: events, ops: ops, width: 100, height: 30,
		inflight: map[string]servingReq{}}
	c.spin = spinner.New(spinner.WithSpinner(spinner.Dot))
	if agentLog != "" {
		c.nana = tail.NewReader(agentLog)
	}
	return c
}

// WithEgressLog tails the controller-side egress audit log and renders each new entry
// as a live [EGRESS] line in the [POPO] pane — the console parity for `iceclimber logs`'
// merged feed. Live-only by design: the reader seeds to EOF (the pane shows this run's
// activity, like [POPO]); history is `iceclimber logs`' job.
func (c Console) WithEgressLog(path string) Console {
	if path != "" {
		c.auditTail = tail.NewReader(path)
		_ = c.auditTail.History() // seed to EOF — live entries only
	}
	return c
}

// WithSeedCounts seeds the header counters AND the "last action" time from the durable
// activity log so they reflect the sandbox's real history (and survive a console restart)
// rather than only what this UI process has seen. Passing the zero time for last leaves it
// as "—" until the first live action. Live events increment/advance from here.
func (c Console) WithSeedCounts(served, approved, denied int, last time.Time) Console {
	c.served, c.approved, c.denied, c.lastTS = served, approved, denied, last
	return c
}

// Busy reports whether this console is in a mode that owns the keyboard — an open form or a
// side panel (status/egress). The fleet console consults it for key precedence: a busy member
// keeps its keys (so esc cancels its form), otherwise fleet-nav keys (tabs/overview) win.
func (c Console) Busy() bool { return c.form != nil || c.panel != "" }

// markLastAction advances the header's "last action" time from an event's RFC3339 timestamp.
// Called only for serviced/approved/denied — the operator-facing actions the counters track —
// so the field means "last real activity," not "last event of any kind."
func (c *Console) markLastAction(ts string) {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		c.lastTS = t
	}
}

func (c Console) Init() tea.Cmd { return tea.Batch(c.waitEvent(), tick()) }

func (c Console) waitEvent() tea.Cmd { return func() tea.Msg { return <-c.events } }

func (c Console) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		c.width, c.height = msg.Width, msg.Height
		if c.form != nil {
			c.form = c.form.WithWidth(formWidth(c.width)).WithHeight(formHeight(c.height))
		}
	case tea.KeyMsg:
		// A password prompt (a reconnect re-auth) preempts everything and is answered by
		// typing into a masked field — so it never writes to /dev/tty and corrupts the screen.
		if len(c.pwReqs) > 0 {
			switch msg.String() {
			case "enter":
				c.pwReqs[0].Reply <- c.pwInput
				c.pwReqs, c.pwInput = c.pwReqs[1:], ""
			case "esc", "ctrl+c":
				c.pwReqs[0].Reply <- "" // cancel — the dial fails and the supervisor backs off
				c.pwReqs, c.pwInput = c.pwReqs[1:], ""
			case "backspace":
				if r := []rune(c.pwInput); len(r) > 0 {
					c.pwInput = string(r[:len(r)-1])
				}
			default:
				if msg.Type == tea.KeyRunes {
					c.pwInput += string(msg.Runes)
				}
			}
			return c, nil
		}
		// Input precedence is intentional: an approval modal preempts everything —
		// a held operation (the dispatcher blocked, waiting) is urgent, so it must be
		// answered before a form/panel resumes. A modal can arrive over an open form
		// or panel; answering it returns control to that form/panel unchanged.
		if len(c.modals) > 0 {
			// A held operation must be answered — only y/a/n/d apply. Answering the head
			// reveals the next queued approval (if any) immediately.
			if ch, ok := modalKey(msg.String()); ok {
				c.modals[0].Reply <- ch
				c.modals = c.modals[1:]
			}
			return c, nil
		}
		if c.form != nil {
			return c.updateForm(msg)
		}
		if c.panel != "" {
			return c.updatePanel(msg)
		}
		// While an operator action is in flight only the MUTATING keys (the i/a/b forms —
		// the executor runs one operator action at a time) are blocked below; read-only
		// interaction — scrolling, the status/egress panels, quitting — stays available, so
		// a minutes-long install never locks the operator out of their own console.
		switch msg.String() {
		case "q", "ctrl+c":
			return c, tea.Quit
		case "esc":
			// esc means "back" everywhere else (forms and panels close on it) — at the top
			// level it snaps the feed back to following the newest output. It must NOT
			// quit: a reflexive esc after closing a panel would lose the whole console.
			c.scroll = 0
			return c, nil
		case "up", "k":
			c.scroll++ // scroll back into history (windowRows clamps at the top)
			return c, nil
		case "down", "j":
			if c.scroll > 0 {
				c.scroll--
			}
			return c, nil
		case "pgup":
			c.scroll += 10
			return c, nil
		case "pgdown":
			if c.scroll -= 10; c.scroll < 0 {
				c.scroll = 0
			}
			return c, nil
		case "end", "G":
			c.scroll = 0 // jump back to following the newest
			return c, nil
		case "i":
			if c.ops != nil && c.running == "" {
				return c.openForm("install")
			}
		case "a":
			if c.ops != nil && c.running == "" {
				return c.openForm("agent")
			}
		case "b":
			if c.ops != nil && c.running == "" {
				return c.openForm("bootstrap")
			}
		case "s":
			if c.ops != nil {
				c.panel, c.status = "status", nil
				return c, c.ops.PollStatus()
			}
		case "e":
			if c.ops != nil {
				c.panel, c.cursor, c.panelErr, c.egress = "egress", 0, "", c.ops.Egress()
				return c, nil
			}
		}
	case SandboxMsg:
		// A worker-tagged message: the single-sandbox console just unwraps and dispatches
		// (the ID matters to the fleet shell, which routes by it — see SandboxMsg).
		return c.Update(msg.Msg)
	case StatusMsg:
		s := StatusSnapshot(msg)
		c.status = &s
		return c, nil
	case activity.Event:
		wasServing := len(c.inflight) != 0
		c.applyEvent(msg)
		// A request just entered service → start the spinner if it isn't already running.
		if len(c.inflight) != 0 && !wasServing {
			return c, tea.Batch(c.waitEvent(), c.tickSpinner())
		}
		return c, c.waitEvent()
	case *ApprovalRequest:
		c.modals = append(c.modals, msg)
		return c, c.waitEvent()
	case *PasswordRequest:
		if len(c.pwReqs) == 0 {
			c.pwInput = ""
		}
		c.pwReqs = append(c.pwReqs, msg)
		return c, c.waitEvent()
	case ConnStateMsg:
		c.connState = msg.State
		return c, c.waitEvent()
	case ClearServingMsg:
		if msg.ID == "" {
			c.inflight = map[string]servingReq{} // defensive: clear all
		} else {
			delete(c.inflight, msg.ID)
		}
		if len(c.inflight) == 0 {
			c.servingProg = nil
		}
		return c, c.waitEvent()
	case HeartbeatMsg:
		c.lastHeartbeat, c.heartbeatSeq = msg.At, msg.Seq
		return c, c.waitEvent()
	case ProgressMsg:
		m := msg
		// Route by ORIGIN, not by what's running: an operator action and an agent request
		// can transfer concurrently, so an agent sample must reach the serving banner even
		// while an operator op owns the footer meter (#3).
		if msg.Agent {
			c.servingProg = &m
		} else {
			if msg.Phase != c.progPhase() {
				c.progStart = time.Now() // new phase → reset the ETA clock
			}
			c.prog = &m
		}
		return c, c.waitEvent()
	case spinner.TickMsg:
		if c.running == "" && len(c.inflight) == 0 {
			c.spinning = false // both idle → stop the loop (restarted when an action begins)
			return c, nil
		}
		var cmd tea.Cmd
		c.spin, cmd = c.spin.Update(msg)
		return c, cmd
	case OpResultMsg:
		c.running = ""
		c.prog = nil
		return c, nil
	case tickMsg:
		if c.nana != nil {
			for _, l := range c.nana.Poll() {
				c.addNana(l)
			}
		}
		if c.auditTail != nil {
			for _, l := range c.auditTail.Poll() {
				c.addEgress(l)
			}
		}
		return c, tick()
	default:
		// huh's own messages (cursor blink, etc.) while a form is open.
		if c.form != nil {
			return c.updateForm(msg)
		}
	}
	return c, nil
}

// updatePanel handles keys while a read/manage panel (status / egress) is open.
func (c Console) updatePanel(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		c.panel, c.status = "", nil
		return c, nil
	case "ctrl+c":
		return c, tea.Quit
	}
	switch c.panel {
	case "status":
		if msg.String() == "r" {
			return c, c.ops.PollStatus() // manual refresh
		}
	case "egress":
		switch msg.String() {
		case "up", "k":
			if c.cursor > 0 {
				c.cursor--
			}
		case "down", "j":
			if c.cursor < c.egress.rows()-1 {
				c.cursor++
			}
		case "a", "d":
			if p, ok := c.selectedPending(); ok {
				var err error
				if msg.String() == "a" {
					err = c.ops.ApprovePending(p.ID)
				} else {
					err = c.ops.DenyPending(p.ID)
				}
				c.panelErr = errText(err)
				c.egress = c.ops.Egress()
				c.clampCursor()
			}
		case "f":
			if r, ok := c.selectedRule(); ok {
				c.panelErr = errText(c.ops.ForgetRule(r.Kind, r.Pattern))
				c.egress = c.ops.Egress()
				c.clampCursor()
			}
		case "r":
			c.panelErr = ""
			c.egress = c.ops.Egress()
			c.clampCursor()
		}
	}
	return c, nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// selectedPending / selectedRule resolve the cursor against the combined list
// (pending entries first, then rules).
func (c Console) selectedPending() (EgressPending, bool) {
	if c.cursor >= 0 && c.cursor < len(c.egress.Pending) {
		return c.egress.Pending[c.cursor], true
	}
	return EgressPending{}, false
}

func (c Console) selectedRule() (EgressRule, bool) {
	i := c.cursor - len(c.egress.Pending)
	if i >= 0 && i < len(c.egress.Rules) {
		return c.egress.Rules[i], true
	}
	return EgressRule{}, false
}

func (c *Console) clampCursor() {
	n := c.egress.rows()
	if n == 0 {
		c.cursor = 0 // nothing to select; no row highlights (lists render empty)
		return
	}
	if c.cursor >= n {
		c.cursor = n - 1
	}
	if c.cursor < 0 {
		c.cursor = 0
	}
}

func (c Console) View() string {
	if len(c.pwReqs) > 0 {
		return passwordView(c.width, c.height, c.pwReqs[0].Label, len([]rune(c.pwInput)))
	}
	if len(c.modals) > 0 {
		return modalView(c.width, c.height, c.modals[0], len(c.modals)-1)
	}
	switch c.panel {
	case "status":
		return statusView(c.width, c.height, c.sandboxID, c.status)
	case "egress":
		return egressView(c.width, c.height, c.egress, c.cursor, c.panelErr)
	}
	if c.form != nil {
		return formView(c.width, c.height, c.formTitle(), c.form.View())
	}
	meter := ""
	if c.running != "" {
		meter = c.renderMeter()
	}
	serving := ""
	if len(c.inflight) != 0 {
		serving = c.renderServing()
	}
	hb := hbStatus{}
	if !c.lastHeartbeat.IsZero() {
		hb = hbStatus{known: true, seq: c.heartbeatSeq, age: time.Since(c.lastHeartbeat)}
	}
	return dashboard(c.width, c.height, c.sandboxID, c.served, c.approved, c.denied,
		c.lastTS, c.connState, hb, serving, c.popoLines, c.nanaLines, true, c.nana != nil, c.ops != nil, c.running, meter, c.scroll)
}

// openForm builds and focuses the named operator form.
func (c Console) openForm(kind string) (tea.Model, tea.Cmd) {
	c.formKind = kind
	switch kind {
	case "install":
		c.form = c.installForm()
	case "agent":
		c.form = c.agentForm()
	case "bootstrap":
		c.form = c.bootstrapForm()
	}
	c.form = c.form.WithWidth(formWidth(c.width)).WithHeight(formHeight(c.height)).WithShowHelp(true)
	return c, c.form.Init()
}

// updateForm delegates a message to the active form and acts on completion/abort.
func (c Console) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	f, cmd := c.form.Update(msg)
	if ff, ok := f.(*huh.Form); ok {
		c.form = ff
	}
	switch c.form.State {
	case huh.StateCompleted:
		kind := c.formKind
		c.form, c.formKind = nil, ""
		return c.submitForm(kind)
	case huh.StateAborted:
		c.form, c.formKind = nil, ""
		return c, nil
	}
	return c, cmd
}

// submitForm kicks off the operator action the completed form described.
func (c Console) submitForm(kind string) (tea.Model, tea.Cmd) {
	if c.st == nil {
		return c, nil
	}
	switch kind {
	case "install":
		c.running = installLabel(c.st.lang)
		c.progStart = time.Now()
		// Persist the managed-vs-system choice for the selected language before installing, so
		// ensurePython/ensureNode/ensureJava resolve the intended source (runtimeSourcesNow reads
		// it fresh — no reconnect). Only written when the box actually offers a system runtime for
		// this language (else rtSource is absent and the persisted/managed default stands).
		if lang := runtimeLangFor(c.st.lang); c.st.rtSource[lang] != nil {
			sel := RuntimeSelection{System: *c.st.rtSource[lang] == "system"}
			if lang == "python" && sel.System && c.st.pyEnvManager == "conda" {
				sel.EnvManager = "conda"
			}
			_ = c.ops.SetRuntimeSources(map[string]RuntimeSelection{lang: sel})
		}
		// Batch the spinner tick so the in-flight footer animates while the op runs.
		return c, tea.Batch(c.ops.RunInstall(InstallRequest{
			Lang: c.st.lang, Version: c.st.version, Pkgs: c.st.pkgs, Project: c.st.project, Tool: c.st.tool,
		}), c.tickSpinner())
	case "agent":
		if c.st.agentName == "" {
			return c, nil
		}
		verb := "install"
		if c.st.agentMode == "wrap" {
			verb = "wrap"
		}
		c.running = "agent " + verb
		c.progStart = time.Now()
		return c, tea.Batch(c.ops.RunAgentInstall(AgentInstallRequest{
			Name:     c.st.agentName,
			Wrap:     c.st.agentMode == "wrap",
			Bin:      c.st.agentBin,
			SkipAuth: c.st.agentAuth == "skip",
		}), c.tickSpinner())
	case "bootstrap":
		if !c.st.confirm {
			return c, nil // operator declined at the confirm
		}
		c.running = "bootstrap"
		c.progStart = time.Now()
		if c.st.bootstrapAction == "reset" {
			return c, tea.Batch(c.ops.RunBootstrapReset(), c.tickSpinner())
		}
		return c, tea.Batch(c.ops.RunBootstrap(), c.tickSpinner())
	}
	return c, nil
}

// tickSpinner starts the spinner animation loop, but only if one isn't already live —
// so a concurrent operator action (`running`) and agent request (`serving`) share a
// single tick loop instead of double-animating (two loops would spin at 2× speed). The
// loop stops itself in the spinner.TickMsg handler once both indicators are idle.
func (c *Console) tickSpinner() tea.Cmd {
	if c.spinning {
		return nil
	}
	c.spinning = true
	return c.spin.Tick
}

// progPhase is the current progress phase, or "" when none.
func (c Console) progPhase() string {
	if c.prog == nil {
		return ""
	}
	return c.prog.Phase
}

// renderMeter builds the in-flight footer line: spinner + action label + current
// phase, with a byte bar/%/ETA (transfers) or an (i/n) count (packages), and the
// transfer mode. Falls back to spinner + label when no sample has arrived yet.
func (c Console) renderMeter() string {
	b := &strings.Builder{}
	b.WriteString(c.spin.View() + " " + c.running)
	if c.prog == nil {
		b.WriteString(" …")
		return b.String()
	}
	p := c.prog
	b.WriteString(" · " + p.Phase)
	switch {
	case p.Unit == progress.Bytes && p.Total > 0:
		ratio := float64(p.Cur) / float64(p.Total)
		fmt.Fprintf(b, "  %s %d%%  %s/%s", meterBar(ratio, 18), pct(ratio),
			progress.HumanBytes(p.Cur), progress.HumanBytes(p.Total))
		if eta := progress.ETA(p.Cur, p.Total, time.Since(c.progStart)); eta != "" {
			b.WriteString("  " + eta)
		}
	case p.Unit == progress.Items && p.Total > 0:
		fmt.Fprintf(b, "  (%d/%d)", p.Cur, p.Total)
	}
	if p.Transport != "" {
		b.WriteString(" · via " + p.Transport)
	}
	return b.String()
}

// renderServing builds the pinned in-flight banner for the request being serviced:
// the spinner + type + elapsed (servingLine), with a byte bar/%/ETA or (i/n) count
// appended once a transfer sample for the agent request has arrived (#3).
func (c Console) renderServing() string {
	n := len(c.inflight)
	if n == 0 {
		return ""
	}
	if n > 1 {
		// Several in flight (concurrent dispatch): summarize by count + type. Sort the type list
		// so the banner is stable (deterministic for tests, no flicker on map iteration order).
		types := make([]string, 0, n)
		for _, r := range c.inflight {
			types = append(types, r.typ)
		}
		slices.Sort(types)
		return servingSummary(c.spin.View(), n, types)
	}
	// Exactly one — the detailed per-request meter.
	var only servingReq
	for _, r := range c.inflight {
		only = r
	}
	line := servingLine(c.spin.View(), only.typ, time.Since(only.start))
	p := c.servingProg
	if p == nil {
		return line
	}
	b := &strings.Builder{}
	b.WriteString(line)
	switch {
	case p.Unit == progress.Bytes && p.Total > 0:
		ratio := float64(p.Cur) / float64(p.Total)
		fmt.Fprintf(b, "  %s %d%%  %s/%s", meterBar(ratio, 18), pct(ratio),
			progress.HumanBytes(p.Cur), progress.HumanBytes(p.Total))
		if eta := progress.ETA(p.Cur, p.Total, time.Since(only.start)); eta != "" {
			b.WriteString("  " + eta)
		}
	case p.Unit == progress.Items && p.Total > 0:
		fmt.Fprintf(b, "  (%d/%d)", p.Cur, p.Total)
	}
	if p.Transport != "" {
		b.WriteString(" · via " + p.Transport)
	}
	return b.String()
}

// installForm picks a runtime to install and — capabilities-aware — how to source it. When
// the box has a system runtime for the chosen language, the operator chooses managed (an
// iceclimber-owned runtime) vs system (the detected binary, packages/envs still isolated
// under $ICECLIMBER_HOME); a system python additionally picks venv vs conda when the box has
// conda. This is the post-bootstrap home for the source choice — bootstrap no longer asks.
// RuntimeLanguage is one managed-runtime option the install form offers. Value is the form
// value the CLI layer maps to a runtime (note "javascript" is the node runtime's label — see
// the canonical mapping in the cli layer); Label is the human option text.
type RuntimeLanguage struct{ Value, Label string }

// RuntimeInstallLanguages are the managed-runtime options the install form offers, in display
// order. It's exported (and the source the form iterates) so a parity test can assert that every
// managed runtime the controller can install has a console path — the mandatory TUI/cmdline
// parity rule turned into an enforced check rather than a convention someone must remember.
func RuntimeInstallLanguages() []RuntimeLanguage {
	return []RuntimeLanguage{
		{"python", "Python"},
		{"javascript", "JavaScript"},
		{"java", "Java"},
		{"rust", "Rust"},
	}
}

func (c *Console) installForm() *huh.Form {
	c.st = &formState{lang: "python", rtSource: map[string]*string{}}
	// The managed-runtime options come from RuntimeInstallLanguages so a parity test can assert
	// every language the controller can install has a console path (TUI/cmdline parity), then the
	// non-runtime entries (shell, catalog tool) are appended.
	langOpts := make([]huh.Option[string], 0, len(RuntimeInstallLanguages())+2)
	for _, rl := range RuntimeInstallLanguages() {
		langOpts = append(langOpts, huh.NewOption(rl.Label, rl.Value))
	}
	langOpts = append(langOpts,
		huh.NewOption("Shell (bash — for `iceclimber shell`)", "shell"),
		huh.NewOption("Tool (a catalog utility: jq, fd, …)", "tool"),
	)
	groups := []*huh.Group{huh.NewGroup(
		huh.NewSelect[string]().Title("language").Value(&c.st.lang).Options(langOpts...),
	)}
	// Tool picker — shown only for the "tool" language; a Select over the catalog so the
	// operator installs a utility the same way the CLI's `install tool` + agent's
	// `tool.install` do (TUI/cmdline parity).
	if tools := c.ops.CatalogTools(); len(tools) > 0 {
		opts := make([]huh.Option[string], len(tools))
		for i, t := range tools {
			opts[i] = huh.NewOption(fmt.Sprintf("%s %s — %s", t.Name, t.Version, t.Description), t.Name)
		}
		c.st.tool = tools[0].Name
		groups = append(groups, huh.NewGroup(huh.NewSelect[string]().
			Title("tool").Description("A checksum-verified static utility, installed onto the sandbox PATH.").
			Value(&c.st.tool).Options(opts...),
		).WithHideFunc(func() bool { return c.st.lang != "tool" }))
	}
	// One source page per detected system runtime, shown only when the chosen language matches
	// it (huh hides at the group level, not the field level) — so the wizard is language →
	// source → [python env] → packages, skipping runtimes the box can't source from the system.
	var pyHasConda bool
	for _, rt := range c.ops.DetectedRuntimes() {
		rt := rt
		src := "managed"
		c.st.rtSource[rt.Lang] = &src
		groups = append(groups, huh.NewGroup(huh.NewSelect[string]().
			Title(fmt.Sprintf("%s source", titleCase(rt.Lang))).
			Description(fmt.Sprintf("Detected %s %s at %s. Managed installs an iceclimber-owned runtime; system uses the detected one (packages/envs stay under $ICECLIMBER_HOME).", rt.Lang, rt.Version, rt.Path)).
			Value(c.st.rtSource[rt.Lang]).Options(
			huh.NewOption("managed — iceclimber installs and owns it", "managed"),
			huh.NewOption("system — use the detected runtime", "system"),
		)).WithHideFunc(func() bool { return runtimeLangFor(c.st.lang) != rt.Lang }))
		if rt.Lang == "python" && slices.Contains(rt.EnvManagers, "conda") {
			pyHasConda = true
		}
	}
	// A system python isolates with venv (default) or conda — offered only when the box has
	// conda and the operator picked a system python.
	if pyHasConda {
		c.st.pyEnvManager = "venv"
		pySrc := c.st.rtSource["python"]
		groups = append(groups, huh.NewGroup(huh.NewSelect[string]().
			Title("python env_manager").
			Description("How a system python isolates packages. Ignored for a managed python.").
			Value(&c.st.pyEnvManager).Options(
			huh.NewOption("venv — standard virtual environment", "venv"),
			huh.NewOption("conda — use the detected conda", "conda"),
		)).WithHideFunc(func() bool { return c.st.lang != "python" || pySrc == nil || *pySrc != "system" }))
	}
	// Packages are optional. The agent — not the operator — decides what to install while it
	// writes code, so an operator install is usually just the bare runtime. Hidden for the
	// shell, which is a single bare binary (no version, no packages).
	groups = append(groups, huh.NewGroup(
		huh.NewInput().Title("packages (optional)").
			Description("Blank installs just the runtime — the agent installs packages as it needs them. Ignored when a project is set.").
			Placeholder("e.g. requests / figlet cli-table3 / com.google.guava:guava:33.0.0-jre").
			Value(&c.st.pkgs),
		huh.NewInput().Title("version (optional)").
			Description("Blank uses the recommended default — Python 3.12 · JavaScript 24 · Java 21 · Rust stable.").
			Placeholder("3.12 / 24 / 21 / stable").Value(&c.st.version),
		huh.NewInput().Title("project (optional)").
			Description("Build a project in this dir instead of installing packages — pom.xml (Java), package.json (JavaScript), Cargo.toml (Rust), or environment.yml (Python, conda env_manager). When set, the packages field is ignored.").
			Placeholder("e.g. /home/iceclimber/proj").Value(&c.st.project),
	).WithHideFunc(func() bool { return c.st.lang == "shell" || c.st.lang == "tool" }))
	return huh.NewForm(groups...)
}

// runtimeLangFor maps an install-form language to the runtime-source language used by the
// store: the JS runtime is persisted as "node", not "javascript"; every other language maps
// to itself. Keeps the install form's labels friendly while the store stays canonical.
func runtimeLangFor(installLang string) string {
	if installLang == "javascript" {
		return "node"
	}
	return installLang
}

// installLabel is the friendly running indicator for an install action.
func installLabel(lang string) string {
	switch lang {
	case "javascript":
		return "JavaScript install"
	case "java":
		return "Java install"
	case "rust":
		return "Rust install"
	case "shell":
		return "shell install"
	case "tool":
		return "tool install"
	default:
		return "Python install"
	}
}

// agentForm collects an agent install/wrap (the nana wrapper). The auth token is read
// from the operator's environment — never typed into the form — so a secret never
// touches the UI; "skip" defers auth. The binary path applies to wrap only.
func (c *Console) agentForm() *huh.Form {
	agents := c.ops.Agents()
	opts := make([]huh.Option[string], 0, len(agents))
	for _, a := range agents {
		opts = append(opts, huh.NewOption(a.DisplayName, a.Name))
	}
	c.st = &formState{agentMode: "install", agentAuth: "env"}
	if len(agents) > 0 {
		c.st.agentName = agents[0].Name
	}
	return huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("agent").Value(&c.st.agentName).Options(opts...),
		huh.NewSelect[string]().Title("how").Value(&c.st.agentMode).Options(
			huh.NewOption("install — relay the agent binary into the sandbox", "install"),
			huh.NewOption("wrap — a binary already on the sandbox (no relay)", "wrap"),
		),
		huh.NewInput().Title("binary path (wrap only, optional)").
			Description("Absolute path on the sandbox; blank = found on PATH. Ignored for install.").
			Value(&c.st.agentBin),
		huh.NewSelect[string]().Title("auth").Value(&c.st.agentAuth).Options(
			huh.NewOption("use the token in my environment", "env"),
			huh.NewOption("skip — configure auth later", "skip"),
		),
	))
}

// bootstrapForm is basic-setup only — provisioning mode + confirm. Runtime source selection
// (managed vs system) is a separate, post-bootstrap concern that lives in the install form.
func (c *Console) bootstrapForm() *huh.Form {
	c.st = &formState{}
	var fields []huh.Field
	// Provisioning mode — the console equivalent of `bootstrap --force`. Default = the safe
	// idempotent re-provision (keeps runtimes); "reset" wipes the sandbox first. A Select
	// (default provision) so it navigates cleanly and never enables a destructive action by
	// accident.
	c.st.bootstrapAction = "provision"
	fields = append(fields, huh.NewSelect[string]().
		Title("Provisioning mode").
		Description("Re-provision is idempotent (installed runtimes are kept). Reset is destructive.").
		Value(&c.st.bootstrapAction).Options(
		huh.NewOption("re-provision — keep installed runtimes/packages", "provision"),
		huh.NewOption("DESTRUCTIVE reset — delete runtimes + state, then reprovision", "reset"),
	))
	fields = append(fields, huh.NewConfirm().Title("Re-provision this sandbox?").
		Description("Ensure the protocol tree, pip.conf, and NANA.md, then run the ping/pong smoke test. (Reset mode wipes runtimes first.)").
		Value(&c.st.confirm))
	return huh.NewForm(huh.NewGroup(fields...))
}

// titleCase upper-cases the first rune of s (for a runtime label like "Node runtime").
func titleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}

func (c Console) formTitle() string {
	switch c.formKind {
	case "bootstrap":
		return "Bootstrap"
	case "agent":
		return "Install agent"
	default:
		return "Install"
	}
}

// clearInflight drops a completed/denied request from the in-flight set; when none remain, the
// agent transfer meter sample is dropped too (it belonged to the request that just finished).
func (c *Console) clearInflight(id string) {
	delete(c.inflight, id)
	if len(c.inflight) == 0 {
		c.servingProg = nil
	}
}

func (c *Console) applyEvent(e activity.Event) {
	// Sandbox echoes are the sandbox's own voice — show them in the [NANA] pane
	// alongside the agent's stream, not in Popo's activity.
	if e.Side == activity.SideNana {
		c.addNana(nanaEcho(e))
		return
	}
	switch e.Kind {
	case activity.KindStarted:
		// Live-only in-progress indicator: add it (keyed by request ID) and stop — it's not a
		// scrollback line and not a counter. The matching serviced/denied removes it.
		c.inflight[e.ID] = servingReq{typ: e.Type, start: time.Now()}
		return
	case activity.KindServiced:
		c.served++
		c.markLastAction(e.TS)
		c.clearInflight(e.ID) // completed → drop from in-progress
	case activity.KindApproved:
		c.approved++
		c.markLastAction(e.TS)
	case activity.KindDenied:
		c.denied++
		c.markLastAction(e.TS)
		c.clearInflight(e.ID) // denied → drop from in-progress
	}
	// Plumbing events (KindOperated reconnect/egress/bootstrap notices, KindVerified) still
	// render in the [POPO] pane below, but they do NOT advance "last action" — that field
	// tracks real serviced/approved/denied activity, not connection churn (else it reads as a
	// meaningless countdown from startup).
	c.popoLines = append(c.popoLines, eventToLine(e))
	if len(c.popoLines) > maxLines {
		c.popoLines = c.popoLines[len(c.popoLines)-maxLines:]
	}
}

// addEgress renders one audit-log JSONL line as a live [EGRESS] line in the [POPO] pane
// (the console parity for `iceclimber logs`). Garbled lines are skipped.
func (c *Console) addEgress(raw string) {
	var e audit.Entry
	if json.Unmarshal([]byte(raw), &e) != nil {
		return
	}
	c.popoLines = append(c.popoLines, popoLine{
		plain: shortTime(e.TS) + "  [EGRESS] " + audit.Body(e),
		style: warnStyle,
	})
	if len(c.popoLines) > maxLines {
		c.popoLines = c.popoLines[len(c.popoLines)-maxLines:]
	}
}

func (c *Console) addNana(raw string) {
	c.nanaLines = append(c.nanaLines, stripANSI(raw))
	if len(c.nanaLines) > maxLines {
		c.nanaLines = c.nanaLines[len(c.nanaLines)-maxLines:]
	}
}

// ModalKey is exported for the fleet console's global approval modal (see ModalView).
func ModalKey(k string) (int, bool) { return modalKey(k) }

func modalKey(k string) (int, bool) {
	switch k {
	case "y":
		return Approve, true
	case "a":
		return ApproveAll, true
	case "n":
		return Deny, true
	case "d":
		return DenyAll, true
	}
	return 0, false
}
