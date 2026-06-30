package remote

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// authMethods assembles the target auth methods in OpenSSH's order: public keys
// (from identity files + ssh-agent), then — only when opted in — keyboard-
// interactive and password. The interactive callbacks are *lazy*: SSH invokes
// them only after key/agent methods fail, so a working agent never triggers a
// prompt. Returns an error only when no method is available at all.
func authMethods(p *dialPlan) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if signers := fileSigners(p.identityFiles); len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	pr := p.prompter
	if pr == nil {
		pr = ttyPrompter{}
	}
	// Keyboard-interactive uses the raw prompter even behind a CachingPrompter: its
	// challenges may be one-time OTP/2FA codes that must not be replayed on reconnect.
	// Password (PAM/local) may ride the cache.
	kbdPr := pr
	if cp, ok := pr.(*CachingPrompter); ok {
		kbdPr = cp.Raw()
	}
	if p.allowKbd {
		methods = append(methods, keyboardInteractiveAuth(kbdPr))
	}
	if p.allowPassword {
		methods = append(methods, passwordAuth(pr, p.user+"@"+p.host))
	}

	if len(methods) == 0 {
		return nil, errors.New("no SSH auth method available: set ssh.identity_file, start ssh-agent (SSH_AUTH_SOCK), or enable ssh.password_auth")
	}
	return methods, nil
}

// IsAuthFailure reports whether err is an SSH authentication failure (the server
// rejected our credentials) rather than a transport/network error. The reconnect
// supervisor uses it to decide whether to drop a cached password and re-prompt
// (auth failure) or keep it and retry (transport failure). Matches the stable
// x/crypto/ssh handshake-failure message.
func IsAuthFailure(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unable to authenticate")
}

// fileSigners loads private keys from the given files, in order, deduped. Missing
// or unreadable files are skipped silently (ssh -G emits nonexistent defaults),
// and passphrase-protected/unparseable keys are skipped too (the agent likely
// holds them) — matching how OpenSSH tries each identity and moves on.
func fileSigners(files []string) []ssh.Signer {
	var signers []ssh.Signer
	seen := map[string]bool{}
	for _, f := range files {
		f = expandTilde(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		key, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		s, err := ssh.ParsePrivateKey(key)
		if err != nil {
			continue
		}
		signers = append(signers, s)
	}
	return signers
}

// keyboardInteractiveAuth answers each server challenge via the prompter (one
// no-echo read per question — covers password and 2FA/OTP prompts).
func keyboardInteractiveAuth(pr PasswordPrompter) ssh.AuthMethod {
	return ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
		return kbdAnswers(pr, questions)
	})
}

// kbdAnswers maps N challenge questions to N prompter answers (extracted so the
// challenge logic is unit-testable without a live SSH server).
func kbdAnswers(pr PasswordPrompter, questions []string) ([]string, error) {
	answers := make([]string, len(questions))
	for i, q := range questions {
		ans, err := pr.Prompt(strings.TrimSpace(q) + " ")
		if err != nil {
			return nil, err
		}
		answers[i] = ans
	}
	return answers, nil
}

// passwordAuth prompts (no-echo) for the target password on demand.
func passwordAuth(pr PasswordPrompter, who string) ssh.AuthMethod {
	return ssh.PasswordCallback(func() (string, error) {
		return pr.Prompt(who + "'s password: ")
	})
}

// expandTilde resolves a leading ~ / ~/ against the home dir (ssh -G may emit
// identity/known-hosts paths with ~). ~user is not supported.
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
