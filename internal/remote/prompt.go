package remote

import (
	"fmt"
	"os"
	"sync"

	"golang.org/x/term"
)

// PasswordPrompter reads a secret for interactive SSH auth (password /
// keyboard-interactive). It is an injectable seam so tests can drive the auth
// flow without a terminal.
type PasswordPrompter interface {
	// Prompt writes label to the terminal and reads one line without echo.
	Prompt(label string) (string, error)
}

// ttyPrompter reads from the controlling terminal via /dev/tty (NOT stdin), so a
// password works even when stdin/stdout are redirected — i.e. in iceclimber's
// headless-serve mode. It never echoes. Only a truly non-interactive environment
// (no controlling tty at all — cron/CI/daemon) fails, with an actionable message;
// in that case use ssh-agent or key-based auth instead.
type ttyPrompter struct{}

func (ttyPrompter) Prompt(label string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("password authentication needs a terminal (no /dev/tty); run interactively, start ssh-agent, or use key-based auth: %w", err)
	}
	defer tty.Close()

	fmt.Fprint(tty, label)
	secret, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(tty) // newline the no-echo read swallowed
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(secret), nil
}

// CachingPrompter wraps a prompter with a two-phase in-memory cache so a reconnect
// can re-authenticate with the password the operator typed once, without re-asking.
// Only a password that actually authenticated is reused: Prompt stages the entry as
// *pending*; the caller Commits it after a successful dial. A wrong first entry — or
// a previously-good password that later stops working — is dropped with Forget and
// re-prompted. The secret lives only here in memory: never written to disk, never
// logged, gone when the process exits.
//
// Caching is for password auth only. Keyboard-interactive challenges may be one-time
// OTP/2FA codes that must not be replayed, so authMethods routes those through Raw().
type CachingPrompter struct {
	inner PasswordPrompter

	mu        sync.Mutex
	committed string
	hasCommit bool
	pending   string
	hasPend   bool
}

// NewCachingPrompter wraps inner; a nil inner defaults to the /dev/tty prompter.
func NewCachingPrompter(inner PasswordPrompter) *CachingPrompter {
	if inner == nil {
		inner = ttyPrompter{}
	}
	return &CachingPrompter{inner: inner}
}

// Prompt returns the committed secret if one exists (silent reconnect); otherwise it
// reads via the inner prompter and stages the answer as pending (not yet reusable).
func (c *CachingPrompter) Prompt(label string) (string, error) {
	c.mu.Lock()
	if c.hasCommit {
		v := c.committed
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()

	v, err := c.inner.Prompt(label) // outside the lock: this blocks on the tty
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.pending, c.hasPend = v, true
	c.mu.Unlock()
	return v, nil
}

// Commit promotes a pending secret to committed, so future prompts reuse it. Called
// after a successful dial. No-op when nothing is pending (e.g. key/agent auth).
func (c *CachingPrompter) Commit() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hasPend {
		c.committed, c.hasCommit = c.pending, true
		c.pending, c.hasPend = "", false
	}
}

// Forget drops both pending and committed secrets so the next Prompt re-reads.
func (c *CachingPrompter) Forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.committed, c.hasCommit = "", false
	c.pending, c.hasPend = "", false
}

// Raw returns the underlying (uncached) prompter — used for keyboard-interactive,
// whose challenges may be one-time codes that must never be replayed.
func (c *CachingPrompter) Raw() PasswordPrompter { return c.inner }
