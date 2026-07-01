package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

// Reconnect backoff: exponential from 1s, capped at 30s, reset after a healthy
// serve cycle. The loop retries indefinitely until ctx is cancelled — a transient
// drop never self-terminates serve.
const (
	reconnectBackoffInitial = 1 * time.Second
	reconnectBackoffMax     = 30 * time.Second
)

// serveHooks observe the supervised loop's connection lifecycle. All optional.
// onConnected receives the freshly-opened session and the attempt count (0 on the
// first connect, >0 after a reconnect) — the console stores the session in its
// holder and flips its header. onDown fires when a serve cycle ends with a transient
// error, just before backing off.
type serveHooks struct {
	onConnected func(sess *session, attempt int)
	onDown      func(err error, attempt int, backoff time.Duration)
}

// superviseServe runs the long-lived serve loop with auto-reconnect, shared by the
// headless `serve`/`runHeadless` path and the TUI console. It opens a session,
// serves until Serve returns, and — unless ctx was cancelled — reconnects with
// capped exponential backoff, retrying forever.
//
// Reconnect reuses the same DialConfig, so key/agent auth re-auths silently. For
// password auth a single CachingPrompter is threaded through every dial: the
// password the operator typed once is Committed on a successful dial and reused on
// reconnect; an auth failure Forgets it so the next attempt re-prompts (a transport
// failure keeps it, so an unattended-but-terminal-launched run reconnects silently).
func superviseServe(ctx context.Context, cfg *config.Config, transport string, deny []string, out io.Writer, supervised bool, interval time.Duration, hooks serveHooks) error {
	prompter := remote.NewCachingPrompter(nil)

	// The bridge reads the live session via the holder, so it follows reconnects;
	// started once (and the agent.log reset once) for the whole serve run.
	holder := &sessionHolder{}
	startAgentLogBridge(ctx, cfg, holder)

	// One connect+serve cycle: dial (reusing the cached prompter), serve until Serve
	// returns, then close. connected reports whether the dial succeeded, so the loop
	// can Commit the password and notify onConnected.
	cycle := func(ctx context.Context, attempt int) (authenticated, served bool, err error) {
		sess, err := openSessionWith(ctx, cfg, transport, prompter)
		if err != nil {
			return false, false, err // dial failed — not authenticated, nothing served
		}
		// Don't serve an unprovisioned sandbox: without the tree, disp.Serve returns instantly
		// and the loop would spin. Fail fast with a clear, non-retryable error (bootstrap is a
		// separate, explicit step).
		if !sess.isBootstrapped(ctx) {
			_ = sess.Close()
			return true, false, notBootstrappedErr(cfg.SandboxID)
		}
		holder.Set(sess)
		if hooks.onConnected != nil {
			hooks.onConnected(sess, attempt)
		}
		// Build the dispatcher first (it installs the interactive approver on the
		// session), then — in proxy egress mode — bring up the reverse-tunneled MITM
		// proxy for this connection so it gates through that same approver. Torn down at
		// cycle end; the next reconnect re-establishes it.
		disp := buildServeDispatcher(ctx, sess, cfg, deny, out, supervised)
		stopProxy, perr := startEgressProxy(ctx, sess, cfg, out)
		if perr != nil {
			// The proxy is an enhancement, not the session: a failed reverse tunnel must NOT
			// tear down the healthy connection (that looped forever). Warn and serve without it
			// — popo install verbs (relay) still work; native-tool egress is disabled.
			fmt.Fprintf(out, "  egress proxy unavailable — native-tool egress disabled this session: %v\n", perr)
			stopProxy = func() {}
		}
		err = disp.Serve(ctx, interval)
		stopProxy()
		_ = sess.Close()
		return true, true, err // reached Serve — a healthy cycle resets backoff
	}
	return runSupervisor(ctx, prompter, cycle, hooks.onDown, sleepCtx)
}

// errNotBootstrapped marks a sandbox that has no iceclimber tree yet. runSupervisor stops
// (rather than reconnect-loops) on it, and serve surfaces the wrapped message.
var errNotBootstrapped = errors.New("sandbox not bootstrapped")

// notBootstrappedErr wraps errNotBootstrapped with an operator-facing message naming the box.
func notBootstrappedErr(sandboxID string) error {
	return fmt.Errorf("sandbox %q is not bootstrapped — run `iceclimber bootstrap` first: %w", sandboxID, errNotBootstrapped)
}

// passwordCache is the Commit/Forget face of CachingPrompter the supervisor drives:
// Commit a password after a successful dial, Forget it after an auth failure.
type passwordCache interface {
	Commit()
	Forget()
}

// cycleFunc attempts one connect+serve cycle. authenticated reports whether the dial
// authenticated (so the loop can Commit the password / handle auth failure); served reports
// whether the cycle got far enough to actually serve (so the loop resets backoff only on
// real progress — a proxy-startup failure is authenticated-but-not-served, and must NOT
// reset backoff or it spins forever at the initial interval).
type cycleFunc func(ctx context.Context, attempt int) (authenticated, served bool, err error)

// runSupervisor is the reconnect control flow, separated from the production
// connect/serve wiring so it can be unit-tested with a fake cycle + sleep. It drives
// the password cache (Commit on connect / Forget on auth failure), classifies a
// cancelled context as a clean stop, and otherwise backs off (capped, exponential)
// and retries forever. sleep returns false when ctx was cancelled while waiting.
func runSupervisor(ctx context.Context, cache passwordCache, cycle cycleFunc, onDown func(err error, attempt int, backoff time.Duration), sleep func(context.Context, time.Duration) bool) error {
	backoff := reconnectBackoffInitial
	attempt := 0
	for {
		authenticated, served, err := cycle(ctx, attempt)
		if authenticated {
			cache.Commit() // the dial authenticated — remember the password
		} else if remote.IsAuthFailure(err) {
			cache.Forget() // bad/stale password — re-prompt on the next attempt
		}
		if served {
			backoff, attempt = reconnectBackoffInitial, 0 // a cycle that actually served resets backoff
		}

		// A cancelled context (Ctrl-C / SIGTERM) is a clean stop, not a drop.
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return nil
		}
		// An unprovisioned sandbox isn't a transient drop — stop with the error rather than
		// reconnect-looping. (The console cycle handles this differently: it waits for an
		// in-place bootstrap instead of returning this.)
		if errors.Is(err, errNotBootstrapped) {
			return err
		}

		// Transient: back off and reconnect.
		attempt++
		if onDown != nil {
			onDown(err, attempt, backoff)
		}
		if !sleep(ctx, backoff) {
			return nil
		}
		if backoff *= 2; backoff > reconnectBackoffMax {
			backoff = reconnectBackoffMax
		}
	}
}

// sleepCtx waits for d or ctx cancellation; it returns false if ctx was cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// loggingServeHooks writes reconnect progress to out — the headless serve feed.
func loggingServeHooks(out io.Writer, sandboxID string) serveHooks {
	return serveHooks{
		onConnected: func(_ *session, attempt int) {
			if attempt > 0 {
				fmt.Fprintf(out, "  reconnected to sandbox %s\n", sandboxID)
			}
		},
		onDown: func(err error, attempt int, backoff time.Duration) {
			fmt.Fprintf(out, "  connection lost: %v — reconnecting in %s (attempt %d)\n",
				err, backoff.Round(time.Second), attempt)
		},
	}
}
