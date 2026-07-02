package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// ShellOpts configures an interactive PTY session (see Interactive.Shell).
type ShellOpts struct {
	// Command is the remote command to exec under the PTY — e.g. the login launcher that
	// sources the sandbox env and execs bash. Required (there is no bare login shell here;
	// the caller always supplies the env-setup + exec).
	Command string
	// Term is the terminal type to request ($TERM); empty falls back to "xterm-256color".
	Term string
	// In/Out/Err are the local terminal streams; nil defaults to os.Stdin/Stdout/Stderr. In
	// must be a terminal (the caller gates on that) — its fd drives PTY size + raw mode.
	In  *os.File
	Out *os.File
	Err *os.File
}

// Interactive is the optional capability of opening an interactive PTY shell over the
// transport. *SSHRunner implements it; callers type-assert (a Runner may be a test fake
// without it). It rides ProxyJump/keepalive/host-key-trust exactly like Run.
type Interactive interface {
	// Shell runs opts.Command under a remote PTY wired to the local terminal, putting the
	// local terminal in raw mode for the duration and forwarding window-resize events. It
	// returns the command's exit status (0 on clean exit). A non-zero exit is NOT an error.
	Shell(ctx context.Context, opts ShellOpts) (exitCode int, err error)
}

var _ Interactive = (*SSHRunner)(nil)

// Shell implements Interactive on the live SSH connection.
func (s *SSHRunner) Shell(ctx context.Context, opts ShellOpts) (int, error) {
	in, out, errOut := opts.In, opts.Out, opts.Err
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	if errOut == nil {
		errOut = os.Stderr
	}
	termType := opts.Term
	if termType == "" {
		termType = "xterm-256color"
	}

	session, err := s.client.NewSession()
	if err != nil {
		return 0, fmt.Errorf("open session: %w", err)
	}
	defer session.Close()

	// Put the local terminal in raw mode so keystrokes (incl. Ctrl-C, tab, arrows) flow to
	// the remote pty verbatim; restore on the way out no matter how we exit.
	fd := int(in.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return 0, fmt.Errorf("set raw terminal mode: %w", err)
	}
	defer func() { _ = term.Restore(fd, state) }()

	w, h, err := term.GetSize(fd)
	if err != nil || w <= 0 || h <= 0 {
		w, h = 80, 24 // sane default if the size can't be read
	}
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty(termType, h, w, modes); err != nil {
		return 0, fmt.Errorf("request pty: %w", err)
	}

	session.Stdin = in
	session.Stdout = out
	session.Stderr = errOut

	// Forward SIGWINCH → remote window-change so a resize reflows the remote shell.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			if nw, nh, e := term.GetSize(fd); e == nil {
				_ = session.WindowChange(nh, nw)
			}
		}
	}()

	// Cancel closes the session so Wait unblocks (Ctrl-C itself goes to the remote as a raw
	// keystroke, not a local signal, since raw mode suppresses local signal generation).
	done := make(chan error, 1)
	go func() { done <- session.Run(opts.Command) }()
	select {
	case <-ctx.Done():
		_ = session.Close()
		<-done
		return 0, ctx.Err()
	case runErr := <-done:
		var exitErr *ssh.ExitError
		if errors.As(runErr, &exitErr) {
			return exitErr.ExitStatus(), nil
		}
		if runErr != nil {
			// A closed-by-remote or missing-exit signal on a shell exit isn't a controller
			// error; surface only genuine transport failures.
			var missing *ssh.ExitMissingError
			if errors.As(runErr, &missing) {
				return 0, nil
			}
			return 0, fmt.Errorf("interactive session: %w", runErr)
		}
		return 0, nil
	}
}
