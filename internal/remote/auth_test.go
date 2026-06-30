package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
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

type fakePrompter struct{ secret string }

func (f fakePrompter) Prompt(string) (string, error) { return f.secret, nil }
