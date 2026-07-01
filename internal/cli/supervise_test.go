package cli

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestSessionHolder_ConcurrentGetSet locks in the documented race-safety: the
// supervisor Sets a fresh session on reconnect while consoleOps + the bridge Get the
// current one. Run under -race; the assertion is "no data race / no panic".
func TestSessionHolder_ConcurrentGetSet(t *testing.T) {
	h := &sessionHolder{}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				h.Set(&session{sandboxID: "s"})
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = h.Get()
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// fakeCache records the supervisor's Commit/Forget decisions.
type fakeCache struct{ commits, forgets int }

func (f *fakeCache) Commit() { f.commits++ }
func (f *fakeCache) Forget() { f.forgets++ }

// instantSleep skips the real backoff wait but still honors cancellation, and
// records each requested backoff so the test can assert the schedule.
func instantSleep(sleeps *[]time.Duration) func(context.Context, time.Duration) bool {
	return func(ctx context.Context, d time.Duration) bool {
		*sleeps = append(*sleeps, d)
		return ctx.Err() == nil
	}
}

// TestSupervisor_TransientThenReconnect: two transient failures back off (1s, 2s)
// and reconnect; the third cycle connects (Commit) and the loop stops cleanly when
// ctx is cancelled.
func TestSupervisor_TransientThenReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := &fakeCache{}
	var sleeps []time.Duration
	calls := 0
	cycle := func(_ context.Context, _ int) (authenticated, served bool, err error) {
		calls++
		switch calls {
		case 1, 2:
			return false, false, errors.New("connection reset by peer") // transient
		default:
			cancel() // connected; the serve cycle then sees ctx cancellation
			return true, true, context.Canceled
		}
	}
	if err := runSupervisor(ctx, cache, cycle, nil, instantSleep(&sleeps)); err != nil {
		t.Fatalf("runSupervisor returned %v, want nil (clean stop)", err)
	}
	if calls != 3 {
		t.Errorf("cycle ran %d times, want 3", calls)
	}
	if cache.commits != 1 || cache.forgets != 0 {
		t.Errorf("commits=%d forgets=%d, want 1/0 (connect commits, no auth failure)", cache.commits, cache.forgets)
	}
	want := []time.Duration{reconnectBackoffInitial, 2 * reconnectBackoffInitial}
	if len(sleeps) != len(want) || sleeps[0] != want[0] || sleeps[1] != want[1] {
		t.Errorf("backoff schedule = %v, want %v", sleeps, want)
	}
}

// TestSupervisor_BackoffResetsAfterHealthyCycle: a successful connect (Commit) resets
// the backoff, so a drop after a healthy cycle starts over at 1s rather than continuing
// to grow. Guards the `backoff, attempt = initial, 0` reset against silent regression.
func TestSupervisor_BackoffResetsAfterHealthyCycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := &fakeCache{}
	var sleeps []time.Duration
	calls := 0
	cycle := func(_ context.Context, _ int) (authenticated, served bool, err error) {
		calls++
		switch calls {
		case 1, 2:
			return false, false, errors.New("connection reset by peer") // grows backoff: 1s, 2s
		case 3:
			return true, true, nil // connected + served a full cycle, then ended cleanly → reset
		default:
			cancel()
			return false, false, context.Canceled
		}
	}
	if err := runSupervisor(ctx, cache, cycle, nil, instantSleep(&sleeps)); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{reconnectBackoffInitial, 2 * reconnectBackoffInitial, reconnectBackoffInitial}
	if len(sleeps) < 3 || sleeps[0] != want[0] || sleeps[1] != want[1] || sleeps[2] != want[2] {
		t.Errorf("backoff did not reset after a healthy cycle: got %v, want prefix %v", sleeps, want)
	}
	if cache.commits != 1 {
		t.Errorf("commits=%d, want 1 (the healthy cycle commits the password)", cache.commits)
	}
}

// TestSupervisor_NotBootstrappedStops: an unprovisioned sandbox is not a transient drop —
// runSupervisor must STOP with the error, not reconnect-loop.
func TestSupervisor_NotBootstrappedStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := &fakeCache{}
	var sleeps []time.Duration
	calls := 0
	cycle := func(_ context.Context, _ int) (authenticated, served bool, err error) {
		calls++
		return true, false, notBootstrappedErr("box-1")
	}
	err := runSupervisor(ctx, cache, cycle, nil, instantSleep(&sleeps))
	if !errors.Is(err, errNotBootstrapped) {
		t.Errorf("runSupervisor should return the not-bootstrapped error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("cycle ran %d times, want 1 (no reconnect loop)", calls)
	}
	if len(sleeps) != 0 {
		t.Errorf("not-bootstrapped must not back off/retry; got sleeps=%v", sleeps)
	}
}

// TestSupervisor_AuthenticatedNotServedEscalates guards the runSupervisor invariant: a cycle
// that authenticates but never serves (authenticated=true, served=false) must NOT reset the
// backoff (else it would spin forever at the initial interval); the password is still
// Committed, but the backoff grows 1s→2s→4s. (A failed egress proxy no longer produces this
// shape — it degrades and serves — but the invariant still protects any future not-served path.)
func TestSupervisor_AuthenticatedNotServedEscalates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := &fakeCache{}
	var sleeps []time.Duration
	calls := 0
	cycle := func(_ context.Context, _ int) (authenticated, served bool, err error) {
		calls++
		if calls >= 4 {
			cancel()
			return true, false, context.Canceled
		}
		return true, false, errors.New("egress reverse tunnel: port 18080 is still forwarded") // authenticated, never served
	}
	if err := runSupervisor(ctx, cache, cycle, nil, instantSleep(&sleeps)); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{reconnectBackoffInitial, 2 * reconnectBackoffInitial, 4 * reconnectBackoffInitial}
	if len(sleeps) < 3 || sleeps[0] != want[0] || sleeps[1] != want[1] || sleeps[2] != want[2] {
		t.Errorf("proxy-startup failure did not escalate backoff (it must not reset on authenticated-but-not-served): got %v, want prefix %v", sleeps, want)
	}
	if cache.commits == 0 {
		t.Error("an authenticated cycle should still Commit the password even when it never served")
	}
}

// TestSupervisor_AuthFailureForgets: a dial that fails with an auth error makes the
// supervisor Forget the cached password so the next attempt re-prompts.
func TestSupervisor_AuthFailureForgets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := &fakeCache{}
	var sleeps []time.Duration
	calls := 0
	cycle := func(_ context.Context, _ int) (authenticated, served bool, err error) {
		calls++
		if calls == 1 {
			// the shape remote.IsAuthFailure matches
			return false, false, errors.New("ssh handshake h:22: ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey]")
		}
		cancel()
		return false, false, context.Canceled
	}
	if err := runSupervisor(ctx, cache, cycle, nil, instantSleep(&sleeps)); err != nil {
		t.Fatalf("runSupervisor returned %v, want nil", err)
	}
	if cache.forgets != 1 {
		t.Errorf("forgets=%d, want 1 (auth failure should drop the cached password)", cache.forgets)
	}
	if cache.commits != 0 {
		t.Errorf("commits=%d, want 0 (never connected)", cache.commits)
	}
}

// TestSupervisor_CanceledStopsImmediately: when the very first cycle returns because
// ctx was cancelled, the loop stops without backing off.
func TestSupervisor_CanceledStopsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cache := &fakeCache{}
	var sleeps []time.Duration
	cycle := func(_ context.Context, _ int) (authenticated, served bool, err error) {
		return false, false, context.Canceled
	}
	if err := runSupervisor(ctx, cache, cycle, nil, instantSleep(&sleeps)); err != nil {
		t.Fatalf("runSupervisor returned %v, want nil", err)
	}
	if len(sleeps) != 0 {
		t.Errorf("a cancelled context must not back off; got sleeps=%v", sleeps)
	}
}

// TestSupervisor_BackoffCaps: sustained transient failures cap the backoff at
// reconnectBackoffMax (no unbounded growth).
func TestSupervisor_BackoffCaps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := &fakeCache{}
	var sleeps []time.Duration
	calls := 0
	cycle := func(_ context.Context, _ int) (authenticated, served bool, err error) {
		calls++
		if calls >= 12 {
			cancel()
			return false, false, context.Canceled
		}
		return false, false, errors.New("network unreachable")
	}
	if err := runSupervisor(ctx, cache, cycle, nil, instantSleep(&sleeps)); err != nil {
		t.Fatal(err)
	}
	last := sleeps[len(sleeps)-1]
	if last != reconnectBackoffMax {
		t.Errorf("backoff capped at %v, got final %v (schedule %v)", reconnectBackoffMax, last, sleeps)
	}
}
