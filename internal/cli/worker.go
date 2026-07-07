package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/tui"
)

// sandboxWorker owns ONE sandbox's serving machinery — the pieces runConsole used to
// hand-wire inline: the caching password prompter (with the pre-alt-screen tty path),
// the initial dial (including the in-console host-key trust flow), the activity logger,
// the session holder, the reconnect supervisor, the agent-log bridge, and the operator
// ops executor. Every UI-bound message it produces is tagged with its sandbox ID
// (tui.SandboxMsg) and lands on its events channel.
//
// This is the fleet unit: today's console runs exactly one; the multi-sandbox console
// runs N of them fanned into one program, routing by the tag.
type sandboxWorker struct {
	cfg    *config.Config
	events chan tea.Msg
	ops    *consoleOps

	// agentLogPath is the controller-side file the [NANA] pane tails (the bridged
	// default, or the operator's explicit --agent-log).
	agentLogPath string
	// seed is the O(1) counters snapshot read before serving started (see SeedCounts).
	seed activity.Snapshot

	cancel   context.CancelFunc
	act      *activity.Logger
	holder   *sessionHolder
	stopOnce sync.Once
}

// startSandboxWorker dials the sandbox (prompting for trust/password on the tty — the
// program isn't on the alt-screen yet), seeds the header counters, and starts the
// agent-log bridge + the reconnect supervisor. On return the worker is serving and every
// subsequent auth prompt routes to the console's masked modal.
// uiReadyAtStart controls where the INITIAL dial's prompts go. false (single-box console, and a
// fleet member started before the alt-screen) → the tty, so trust/password prompts are visible;
// true (a fleet member joining live AFTER the program is up, e.g. a freed serve.lock) → the
// masked modal, and an untrusted host key aborts rather than trying to paint its own alt-screen
// over the running fleet.
func startSandboxWorker(parent context.Context, cfg *config.Config, transport, agentLog string, uiReadyAtStart bool) (*sandboxWorker, error) {
	ctx, cancel := context.WithCancel(parent)
	events := make(chan tea.Msg, 64)

	// A single CachingPrompter is reused for reconnects so a password typed now is remembered
	// (and a single-prompt PAM challenge is reused silently — see kbdAnswers). uiReady gates
	// prompt routing: false → tty (pre-alt-screen), true → the masked modal.
	var uiReady atomic.Bool
	uiReady.Store(uiReadyAtStart)
	prompter := remote.NewCachingPrompter(&tuiPasswordPrompter{
		events: events, done: ctx.Done(), ready: &uiReady, tty: remote.TTYPrompter(),
	})
	sess, err := openSessionWith(parent, cfg, transport, prompter)
	if hke := (*remote.HostKeyError)(nil); errors.As(err, &hke) {
		if uiReadyAtStart {
			// Mid-run join: we can't safely run trustHostInteractive's own alt-screen over the
			// live fleet, so an untrusted key aborts (the member stays ⛔/unreachable).
			cancel()
			return nil, fmt.Errorf("host key for %s not trusted (join a fresh console to record it): %w", cfg.SandboxID, err)
		}
		// First contact with an untrusted (often ephemeral) sandbox: offer to
		// record the host key from within the console, then reconnect once.
		if tErr := trustHostInteractive(parent, cfg, hke); tErr != nil {
			cancel()
			return nil, tErr
		}
		sess, err = openSessionWith(parent, cfg, transport, prompter)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	prompter.Commit()   // the startup dial authenticated — remember the password for reconnects
	uiReady.Store(true) // from here every dial is a reconnect → route its prompt to the modal, never /dev/tty

	w := &sandboxWorker{
		cfg:    cfg,
		events: events,
		cancel: cancel,
		act:    activity.NewSized(activityPath(cfg), cfg.LogMaxBytes, cfg.LogKeep),
		holder: &sessionHolder{},
	}
	w.holder.Set(sess)

	// bootstrapped nudges the serve cycle to re-check + start serving the instant the operator
	// bootstraps in-place, instead of waiting on the reconnect backoff.
	bootstrapped := make(chan struct{}, 1)

	// Every worker emission is tagged with the sandbox ID so a fleet shell can route it;
	// the single-sandbox console just unwraps. Never stall the supervisor on a slow/closed UI.
	emit := func(m tea.Msg) {
		select {
		case events <- tui.SandboxMsg{ID: cfg.SandboxID, Msg: m}:
		default:
		}
	}

	// With no explicit --agent-log, default to the controller-side agent.log and bridge the
	// sandbox's agent stream into it, so [NANA] populates with no flag. The bridge seeds each
	// session.log to its current EOF (streams only NEW output — no re-dump of a prior run) and
	// writes through a rotating logfile.Writer, so we no longer truncate-reset (which would
	// clobber a concurrent writer). The bridge reads the live session via the holder, so it
	// follows reconnects.
	w.agentLogPath = agentLog
	if w.agentLogPath == "" {
		w.agentLogPath = agentLogPath(cfg)
		go bridgeAgentLog(ctx, w.holder, w.agentLogPath, cfg.LogMaxBytes, cfg.LogKeep)
	}

	// Seed the header counters + the "last action" time from the durable activity log
	// (authoritative, cumulative per sandbox) — read NOW, before the serve goroutine appends
	// anything, so a just-serviced event isn't counted twice (seed + the live event on the
	// channel). O(1) from the counts snapshot (backfills once from the log if absent), keeping
	// startup fast regardless of lifetime activity and the counters monotonic across rotation.
	w.seed = activity.SeedCounts(activityPath(cfg))

	// Serve in the background with auto-reconnect. The first cycle serves the already
	// open session; on a drop the supervisor reconnects (capped backoff, forever) and
	// the header reflects the connection state.
	go func() {
		seeded := sess
		cycle := func(ctx context.Context, attempt int) (authenticated, served bool, err error) {
			s := seeded
			seeded = nil
			if s == nil {
				var derr error
				if s, derr = openSessionWith(ctx, cfg, transport, prompter); derr != nil {
					return false, false, derr
				}
			}
			w.holder.Set(s)
			emit(tui.ConnStateMsg{State: tui.ConnConnected})
			if attempt > 0 {
				emit(activity.Event{
					TS: time.Now().UTC().Format(time.RFC3339), Kind: activity.KindOperated,
					Type: "serve", Status: "ok", Detail: "reconnected to sandbox",
				})
			}
			// Don't serve/tunnel an unprovisioned sandbox — that spun the reconnect loop and
			// stormed the password prompt. Surface a clear state and WAIT (no spin) for an
			// in-place bootstrap (the operator presses `b`), which nudges `bootstrapped`.
			if err := awaitBootstrapped(ctx, s.isBootstrapped, bootstrapped, emit); err != nil {
				_ = s.Close()
				return true, false, err
			}
			disp := buildConsoleDispatcher(ctx, s, cfg, w.act, events)
			// In proxy egress mode, bring up the reverse-tunneled MITM proxy for this
			// connection so the sandbox's tools egress through it, gated by the console's
			// interactive approver (set by buildConsoleDispatcher above).
			stopProxy, perr := startEgressProxy(ctx, s, cfg, io.Discard)
			if perr != nil {
				// Don't kill the healthy session when the proxy tunnel can't bind (that looped
				// forever); surface a readable warning and serve without it — relay verbs still work.
				emit(activity.Event{
					TS: time.Now().UTC().Format(time.RFC3339), Kind: activity.KindOperated,
					Type: "egress", Status: "failed",
					Detail: "egress proxy unavailable — native-tool egress disabled this session: " + perr.Error(),
				})
				stopProxy = func() {}
			}
			serveErr := disp.Serve(ctx, 2*time.Second)
			stopProxy()
			_ = s.Close()
			return true, true, serveErr
		}
		onDown := func(err error, attempt int, backoff time.Duration) {
			emit(tui.ConnStateMsg{State: tui.ConnReconnecting})
			emit(activity.Event{
				TS: time.Now().UTC().Format(time.RFC3339), Kind: activity.KindOperated,
				Type: "serve", Status: "failed",
				Detail: fmt.Sprintf("connection lost: %v — reconnecting in %s (attempt %d)", err, backoff.Round(time.Second), attempt),
			})
		}
		_ = runSupervisor(ctx, prompter, cycle, onDown, sleepCtx)
	}()

	w.ops = &consoleOps{ctx: ctx, holder: w.holder, act: w.act, events: events, bootstrapped: bootstrapped}
	return w, nil
}

// stop ends serving (pending approvals fail safe via the ctx done), closes the current
// session, and flushes the activity logger. Idempotent.
func (w *sandboxWorker) stop() {
	w.stopOnce.Do(func() {
		w.cancel()
		if s := w.holder.Get(); s != nil {
			_ = s.Close()
		}
		_ = w.act.Close()
	})
}
