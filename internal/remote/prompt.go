package remote

import (
	"fmt"
	"os"

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
