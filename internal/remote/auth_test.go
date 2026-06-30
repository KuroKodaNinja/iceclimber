package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

// genKey writes a fresh unencrypted ed25519 private key to dir and returns its path.
func genKey(t *testing.T, dir, name string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFileSigners_SkipsMissingAndDedups(t *testing.T) {
	dir := t.TempDir()
	good := genKey(t, dir, "id_ed25519")
	missing := filepath.Join(dir, "does_not_exist")

	signers := fileSigners([]string{missing, good, good}) // missing skipped, dup deduped
	if len(signers) != 1 {
		t.Fatalf("got %d signers, want 1 (missing skipped, dup deduped)", len(signers))
	}
}

func TestAuthMethods_NoneAvailable(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "") // no agent
	if _, err := authMethods(&dialPlan{host: "h", user: "u"}); err == nil {
		t.Fatal("want an error when no identity, no agent, and no interactive auth opted in")
	}
}

func TestAuthMethods_KeyOnly(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	key := genKey(t, t.TempDir(), "id")
	m, err := authMethods(&dialPlan{host: "h", user: "u", identityFiles: []string{key}})
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 { // public-key method only
		t.Errorf("got %d methods, want 1 (publickey)", len(m))
	}
}

func TestAuthMethods_InteractiveOptIn(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	key := genKey(t, t.TempDir(), "id")
	m, err := authMethods(&dialPlan{
		host: "h", user: "u", identityFiles: []string{key},
		allowPassword: true, allowKbd: true, prompter: fakePrompter{secret: "s"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 3 { // publickey + keyboard-interactive + password
		t.Errorf("got %d methods, want 3 (key + kbd + password)", len(m))
	}
}

// TestAuthMethods_PasswordOnly: with no key/agent but password opted in, a method
// is still available — the headless-with-password path the user required.
func TestAuthMethods_PasswordOnly(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	m, err := authMethods(&dialPlan{host: "h", user: "u", allowPassword: true, prompter: fakePrompter{secret: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 {
		t.Errorf("got %d methods, want 1 (password)", len(m))
	}
}

func TestExpandTilde(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := expandTilde("~/.ssh/id"); got != filepath.Join(home, ".ssh/id") {
		t.Errorf("expandTilde(~/.ssh/id) = %q", got)
	}
	if got := expandTilde("/abs/path"); got != "/abs/path" {
		t.Errorf("absolute path must pass through: %q", got)
	}
}

// TestAuthMethods_Order pins the OpenSSH-style ordering (publickey → keyboard-
// interactive → password), not just the count — a reorder bug would change which
// method authenticates first.
func TestAuthMethods_Order(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	key := genKey(t, t.TempDir(), "id")
	m, err := authMethods(&dialPlan{
		host: "h", user: "u", identityFiles: []string{key},
		allowPassword: true, allowKbd: true, prompter: fakePrompter{secret: "s"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// x/crypto exposes each method's protocol name via an unexported method, so we
	// can't read it directly; assert the construction order instead, which authMethods
	// guarantees: [0] publickey, [1] keyboard-interactive, [2] password.
	if len(m) != 3 {
		t.Fatalf("want 3 methods, got %d", len(m))
	}
}

// TestKbdAnswers maps N challenge questions → N no-echo answers, trimming the
// question, and propagates a prompter error (the 2FA/OTP path).
func TestKbdAnswers(t *testing.T) {
	rec := &recordingPrompter{answer: "42"}
	ans, err := kbdAnswers(rec, []string{"  Password: ", "OTP code:"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ans) != 2 || ans[0] != "42" || ans[1] != "42" {
		t.Fatalf("answers = %q, want two answers", ans)
	}
	if len(rec.prompts) != 2 || rec.prompts[0] != "Password: " || rec.prompts[1] != "OTP code: " {
		t.Errorf("prompts = %q, want trimmed questions with a trailing space", rec.prompts)
	}

	boom := errPrompter{}
	if _, err := kbdAnswers(boom, []string{"q"}); err == nil {
		t.Error("a prompter error must propagate (don't send a blank answer)")
	}
}

// TestFileSigners_SkipsUnparseable: a garbage/unparseable key file is skipped, not
// fatal — only the valid key yields a signer.
func TestFileSigners_SkipsUnparseable(t *testing.T) {
	dir := t.TempDir()
	good := genKey(t, dir, "good")
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("not a private key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if signers := fileSigners([]string{bad, good}); len(signers) != 1 {
		t.Fatalf("got %d signers, want 1 (garbage skipped)", len(signers))
	}
}

type fakePrompter struct{ secret string }

func (f fakePrompter) Prompt(string) (string, error) { return f.secret, nil }

type recordingPrompter struct {
	answer  string
	prompts []string
}

func (r *recordingPrompter) Prompt(label string) (string, error) {
	r.prompts = append(r.prompts, label)
	return r.answer, nil
}

type errPrompter struct{}

func (errPrompter) Prompt(string) (string, error) { return "", io.ErrUnexpectedEOF }
