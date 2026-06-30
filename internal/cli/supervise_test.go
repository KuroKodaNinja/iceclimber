package cli

import (
	"context"
	"errors"
	"testing"
	"time"
)

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
	cycle := func(_ context.Context, _ int) (bool, error) {
		calls++
		switch calls {
		case 1, 2:
			return false, errors.New("connection reset by peer") // transient
		default:
			cancel() // connected; the serve cycle then sees ctx cancellation
			return true, context.Canceled
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

// TestSupervisor_AuthFailureForgets: a dial that fails with an auth error makes the
// supervisor Forget the cached password so the next attempt re-prompts.
func TestSupervisor_AuthFailureForgets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := &fakeCache{}
	var sleeps []time.Duration
	calls := 0
	cycle := func(_ context.Context, _ int) (bool, error) {
		calls++
		if calls == 1 {
			// the shape remote.IsAuthFailure matches
			return false, errors.New("ssh handshake h:22: ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey]")
		}
		cancel()
		return false, context.Canceled
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
	cycle := func(_ context.Context, _ int) (bool, error) {
		return false, context.Canceled
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
	cycle := func(_ context.Context, _ int) (bool, error) {
		calls++
		if calls >= 12 {
			cancel()
			return false, context.Canceled
		}
		return false, errors.New("network unreachable")
	}
	if err := runSupervisor(ctx, cache, cycle, nil, instantSleep(&sleeps)); err != nil {
		t.Fatal(err)
	}
	last := sleeps[len(sleeps)-1]
	if last != reconnectBackoffMax {
		t.Errorf("backoff capped at %v, got final %v (schedule %v)", reconnectBackoffMax, last, sleeps)
	}
}
