package remote

import (
	"errors"
	"testing"
)

// queuePrompter returns queued answers and counts how often it was asked, so a
// test can prove when the cache served a value instead of re-prompting.
type queuePrompter struct {
	answers []string
	calls   int
}

func (r *queuePrompter) Prompt(string) (string, error) {
	r.calls++
	if len(r.answers) == 0 {
		return "", errors.New("no more answers queued")
	}
	a := r.answers[0]
	r.answers = r.answers[1:]
	return a, nil
}

// TestCachingPrompter_CommitThenReuse: a committed password is reused silently — the
// inner prompter is asked exactly once across many prompts.
func TestCachingPrompter_CommitThenReuse(t *testing.T) {
	inner := &queuePrompter{answers: []string{"hunter2"}}
	c := NewCachingPrompter(inner)

	got, err := c.Prompt("pw: ")
	if err != nil || got != "hunter2" {
		t.Fatalf("first Prompt = %q,%v; want hunter2", got, err)
	}
	c.Commit() // dial succeeded
	for i := 0; i < 3; i++ {
		if got, _ := c.Prompt("pw: "); got != "hunter2" {
			t.Fatalf("reconnect Prompt = %q, want the committed hunter2", got)
		}
	}
	if inner.calls != 1 {
		t.Errorf("inner prompter asked %d times, want 1 (committed value reused)", inner.calls)
	}
}

// TestCachingPrompter_PendingNotReusedUntilCommit: without Commit, a staged (pending)
// answer is NOT reused — a wrong first entry must be re-asked, not cached.
func TestCachingPrompter_PendingNotReusedUntilCommit(t *testing.T) {
	inner := &queuePrompter{answers: []string{"wrong", "right"}}
	c := NewCachingPrompter(inner)

	if got, _ := c.Prompt("pw: "); got != "wrong" {
		t.Fatalf("first Prompt = %q, want wrong", got)
	}
	// No Commit (the dial failed). The next prompt must re-read, not reuse "wrong".
	if got, _ := c.Prompt("pw: "); got != "right" {
		t.Fatalf("second Prompt = %q, want a fresh read (right)", got)
	}
	if inner.calls != 2 {
		t.Errorf("inner prompter asked %d times, want 2 (pending is not reused)", inner.calls)
	}
}

// TestCachingPrompter_Forget: forgetting a committed secret forces a re-read.
func TestCachingPrompter_Forget(t *testing.T) {
	inner := &queuePrompter{answers: []string{"old", "new"}}
	c := NewCachingPrompter(inner)

	_, _ = c.Prompt("pw: ")
	c.Commit()
	c.Forget() // password changed / auth failed
	if got, _ := c.Prompt("pw: "); got != "new" {
		t.Fatalf("after Forget, Prompt = %q, want a fresh read (new)", got)
	}
	if inner.calls != 2 {
		t.Errorf("inner prompter asked %d times, want 2 (Forget clears the cache)", inner.calls)
	}
}

// TestCachingPrompter_RawIsInner: keyboard-interactive must bypass the cache via Raw
// so one-time OTP/2FA codes are never replayed.
func TestCachingPrompter_RawIsInner(t *testing.T) {
	inner := &queuePrompter{}
	c := NewCachingPrompter(inner)
	if c.Raw() != inner {
		t.Error("Raw() must return the underlying prompter (uncached) for keyboard-interactive")
	}
}

// TestCachingPrompter_NilDefaultsToTTY: a nil inner falls back to the tty prompter.
func TestCachingPrompter_NilDefaultsToTTY(t *testing.T) {
	c := NewCachingPrompter(nil)
	if _, ok := c.Raw().(ttyPrompter); !ok {
		t.Errorf("nil inner should default to ttyPrompter, got %T", c.Raw())
	}
}

// TestPromptersFor_KbdBypassesCache pins the auth routing the OTP-safety property
// depends on: behind a CachingPrompter, the password method gets the cache while the
// keyboard-interactive method gets the raw (uncached) inner — so a committed password
// is reused for reconnect but a kbd challenge (possible one-time code) re-reads every
// time and is never replayed.
func TestPromptersFor_KbdBypassesCache(t *testing.T) {
	inner := &queuePrompter{answers: []string{"pw", "otp1", "otp2"}}
	cp := NewCachingPrompter(inner)

	passwordPr, kbdPr := promptersFor(cp)
	if passwordPr != PasswordPrompter(cp) {
		t.Errorf("password prompter = %T, want the CachingPrompter (cached)", passwordPr)
	}
	if kbdPr != inner {
		t.Errorf("kbd prompter = %T, want the raw inner (uncached)", kbdPr)
	}

	// Password rides the cache: prompt once, commit, then reuse silently.
	if v, _ := passwordPr.Prompt("pw: "); v != "pw" {
		t.Fatalf("password prompt = %q, want pw", v)
	}
	cp.Commit()
	if v, _ := passwordPr.Prompt("pw: "); v != "pw" {
		t.Errorf("committed password not reused: %q", v)
	}
	// Kbd re-reads each time (never the cached "pw") — a fresh OTP every challenge.
	if v, _ := kbdPr.Prompt("code: "); v != "otp1" {
		t.Errorf("kbd answer 1 = %q, want a fresh read (otp1)", v)
	}
	if v, _ := kbdPr.Prompt("code: "); v != "otp2" {
		t.Errorf("kbd answer 2 = %q, want a fresh read (otp2) — never cached", v)
	}
}

// TestPromptersFor_PlainPrompter: a non-caching prompter is used for both paths.
func TestPromptersFor_PlainPrompter(t *testing.T) {
	pr := fakePrompter{secret: "x"}
	pw, kbd := promptersFor(pr)
	if pw != PasswordPrompter(pr) || kbd != PasswordPrompter(pr) {
		t.Errorf("plain prompter should be used for both paths, got pw=%T kbd=%T", pw, kbd)
	}
	pwNil, kbdNil := promptersFor(nil)
	if _, ok := pwNil.(ttyPrompter); !ok {
		t.Errorf("nil → ttyPrompter for password, got %T", pwNil)
	}
	if _, ok := kbdNil.(ttyPrompter); !ok {
		t.Errorf("nil → ttyPrompter for kbd, got %T", kbdNil)
	}
}

// TestIsAuthFailure distinguishes credential rejection from transport errors.
func TestIsAuthFailure(t *testing.T) {
	auth := errors.New("ssh handshake h:22: ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey]")
	if !IsAuthFailure(auth) {
		t.Error("an 'unable to authenticate' error should be classified as an auth failure")
	}
	if IsAuthFailure(errors.New("dial tcp 10.0.0.1:22: connect: connection refused")) {
		t.Error("a transport error must NOT be classified as an auth failure")
	}
	if IsAuthFailure(nil) {
		t.Error("nil is not an auth failure")
	}
}
