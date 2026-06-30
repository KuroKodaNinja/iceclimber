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

	// One connect+serve cycle: dial (reusing the cached prompter), serve until Serve
	// returns, then close. connected reports whether the dial succeeded, so the loop
	// can Commit the password and notify onConnected.
	cycle := func(ctx context.Context, attempt int) (connected bool, err error) {
		sess, err := openSessionWith(ctx, cfg, transport, prompter)
		if err != nil {
			return false, err
		}
		if hooks.onConnected != nil {
			hooks.onConnected(sess, attempt)
		}
		disp := buildServeDispatcher(ctx, sess, cfg, deny, out, supervised)
		err = disp.Serve(ctx, interval)
		_ = sess.Close()
		return true, err
	}
	return runSupervisor(ctx, prompter, cycle, hooks.onDown, sleepCtx)
}

// passwordCache is the slice of CachingPrompter the supervisor drives: Commit a
// password after a successful dial, Forget it after an auth failure.
type passwordCache interface {
	Commit()
	Forget()
}

// cycleFunc attempts one connect+serve cycle, reporting whether the dial connected
// (so the loop can Commit the password) and the resulting error.
type cycleFunc func(ctx context.Context, attempt int) (connected bool, err error)

// runSupervisor is the reconnect control flow, separated from the production
// connect/serve wiring so it can be unit-tested with a fake cycle + sleep. It drives
// the password cache (Commit on connect / Forget on auth failure), classifies a
// cancelled context as a clean stop, and otherwise backs off (capped, exponential)
// and retries forever. sleep returns false when ctx was cancelled while waiting.
func runSupervisor(ctx context.Context, cache passwordCache, cycle cycleFunc, onDown func(err error, attempt int, backoff time.Duration), sleep func(context.Context, time.Duration) bool) error {
	backoff := reconnectBackoffInitial
	attempt := 0
	for {
		connected, err := cycle(ctx, attempt)
		if connected {
			cache.Commit()                                // the dial authenticated — remember the password
			backoff, attempt = reconnectBackoffInitial, 0 // a healthy connection resets backoff
		} else if remote.IsAuthFailure(err) {
			cache.Forget() // bad/stale password — re-prompt on the next attempt
		}

		// A cancelled context (Ctrl-C / SIGTERM) is a clean stop, not a drop.
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return nil
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
