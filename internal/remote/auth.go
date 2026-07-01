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

	passwordPr, _ := promptersFor(p.prompter)
	kbdPr := p.prompter
	if kbdPr == nil {
		kbdPr = ttyPrompter{}
	}
	if p.allowKbd {
		methods = append(methods, keyboardInteractiveAuth(kbdPr))
	}
	if p.allowPassword {
		methods = append(methods, passwordAuth(passwordPr, p.user+"@"+p.host))
	}

	if len(methods) == 0 {
		return nil, errors.New("no SSH auth method available: set ssh.identity_file, start ssh-agent (SSH_AUTH_SOCK), or enable ssh.password_auth")
	}
	return methods, nil
}

// promptersFor resolves the password-method prompter (nil → the /dev/tty prompter). Behind a
// CachingPrompter, password rides the cache. (Keyboard-interactive is handled per-challenge
// in kbdAnswers, which caches only a plain single password prompt.)
func promptersFor(pr PasswordPrompter) (passwordPr, kbdPr PasswordPrompter) {
	if pr == nil {
		pr = ttyPrompter{}
	}
	if cp, ok := pr.(*CachingPrompter); ok {
		return cp, cp.Raw()
	}
	return pr, pr
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

// keyboardInteractiveAuth answers each server challenge via the prompter (one no-echo read
// per question — covers password and 2FA/OTP prompts). pr may be a *CachingPrompter, which
// kbdAnswers uses to cache only a plain single password prompt.
func keyboardInteractiveAuth(pr PasswordPrompter) ssh.AuthMethod {
	return ssh.KeyboardInteractive(func(_, _ string, questions []string, echos []bool) ([]string, error) {
		return kbdAnswers(pr, questions, echos)
	})
}

// kbdAnswers maps N challenge questions to N prompter answers. A single, no-echo challenge is
// a plain password prompt (PAM) — answered via the CACHING prompter so it's committed on
// success and reused on reconnect (matching the SSH password method). Any multi-question or
// echoed challenge is treated as OTP/2FA and answered via the UNCACHED raw prompter, so a
// one-time code is never replayed. Extracted so the logic is unit-testable without a server.
func kbdAnswers(pr PasswordPrompter, questions []string, echos []bool) ([]string, error) {
	answerer := pr
	cacheable := len(questions) == 1 && (len(echos) == 0 || !echos[0])
	if cp, ok := pr.(*CachingPrompter); ok && !cacheable {
		answerer = cp.Raw() // OTP/2FA — never replay a cached answer
	}
	answers := make([]string, len(questions))
	for i, q := range questions {
		ans, err := answerer.Prompt(strings.TrimSpace(q) + " ")
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
