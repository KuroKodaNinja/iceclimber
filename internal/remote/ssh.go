package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHRunner is a Runner backed by a live SSH connection.
type SSHRunner struct {
	client *ssh.Client

	// stopKA stops the keepalive goroutine (if one was started); stopOnce makes
	// stopping idempotent so Close is safe to call more than once.
	stopKA   chan struct{}
	stopOnce sync.Once
}

// keepalive tuning. We send an OpenSSH keepalive every keepAliveDefault and treat
// the link as dead after keepAliveMaxMiss consecutive misses (a reply that errors
// or doesn't arrive within keepAliveDefault) — so a silently-dropped connection is
// detected in roughly interval*maxMiss instead of hanging on the OS TCP timeout.
const (
	keepAliveDefault = 20 * time.Second
	keepAliveMaxMiss = 3
)

// resolveKeepAlive maps a configured interval to the effective one: 0 → the 20s
// default, negative → 0 (disabled), positive → as-is.
func resolveKeepAlive(d time.Duration) time.Duration {
	switch {
	case d == 0:
		return keepAliveDefault
	case d < 0:
		return 0
	default:
		return d
	}
}

// startKeepAlive launches a goroutine that pings the server with
// keepalive@openssh.com every interval. On too many consecutive failures it closes
// the client, so the next fs/Run op fails immediately rather than blocking on the
// kernel's multi-minute TCP timeout. It returns the stop channel the runner owns.
// This rides the ProxyCommand tunnel too, where TCP-level keepalive can't reach.
func startKeepAlive(client *ssh.Client, interval time.Duration) chan struct{} {
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		misses := 0
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				// SendRequest blocks for a reply; bound it so a silently-dead link
				// (no FIN/RST yet) is still caught within ~interval.
				errc := make(chan error, 1)
				go func() {
					_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
					errc <- err
				}()
				select {
				case <-stop:
					return
				case err := <-errc:
					if err != nil {
						misses++
					} else {
						misses = 0
					}
				case <-time.After(interval):
					misses++ // no reply in time — count it as a miss
				}
				if misses >= keepAliveMaxMiss {
					client.Close() // make the next op fail fast
					return
				}
			}
		}
	}()
	return stop
}

// DialConfig is the connection input for Dial. Zero values preserve the original
// direct-dial behavior; the SSHConfig/UseSSHConfig fields opt into honoring the
// operator's ~/.ssh/config (and any ProxyJump) via the system ssh client.
type DialConfig struct {
	Host         string
	Port         int
	User         string
	IdentityFile string // optional; falls back to ssh-agent when empty
	KnownHosts   string // optional; defaults to ~/.ssh/known_hosts

	// SSHConfigFile, when set, is passed as `ssh -F <file>` during resolution
	// (power users / hermetic tests); empty uses the default ~/.ssh/config.
	SSHConfigFile string
	// UseSSHConfig gates consulting `ssh -G`. nil/true = consult (honor
	// ~/.ssh/config + ProxyJump); false = force a literal direct dial. (Dial and the
	// rest of the connection path live in dial.go.)
	UseSSHConfig *bool

	// AllowPassword / AllowKeyboardInteractive opt into those interactive auth
	// methods to the target (off by default; key/agent are tried first regardless).
	AllowPassword            bool
	AllowKeyboardInteractive bool
	// Prompter reads secrets for the interactive methods; nil → a /dev/tty no-echo
	// prompter (works headless too, as long as a controlling terminal exists).
	Prompter PasswordPrompter

	// KeepAlive is the interval between SSH keepalive pings (and the TCP keepalive
	// period on a direct dial). Zero uses the 20s default; a negative value disables
	// keepalives entirely. Configured via ssh.keepalive_interval (seconds).
	KeepAlive time.Duration
}

// Run executes cmd in a fresh non-interactive session. No pty is requested — a
// clean byte stream is required for the raw transfers ExecFS relies on (§6).
// When stdin is non-nil it is streamed to the command's standard input.
func (s *SSHRunner) Run(ctx context.Context, cmd string, stdin io.Reader) (Result, error) {
	session, err := s.client.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("open session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if stdin != nil {
		session.Stdin = stdin
	}

	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return Result{}, ctx.Err()
	case runErr := <-done:
		res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
		var exitErr *ssh.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitStatus()
			return res, nil
		}
		if runErr != nil {
			return res, fmt.Errorf("run remote command: %w", runErr)
		}
		return res, nil
	}
}

// Close stops the keepalive goroutine (if any) and closes the underlying SSH
// connection. Safe to call more than once.
func (s *SSHRunner) Close() error {
	s.stopOnce.Do(func() {
		if s.stopKA != nil {
			close(s.stopKA)
		}
	})
	return s.client.Close()
}

// NewSFTP opens an SFTP client over the same SSH connection. The caller owns the
// returned client and must Close it. Fails when the server's SFTP subsystem is
// unavailable — the signal to fall back to ExecFS (§6).
func (s *SSHRunner) NewSFTP() (*sftp.Client, error) {
	return sftp.NewClient(s.client)
}

// RemoteListen requests the SSH server to listen on addr (an `ssh -R` reverse forward)
// and returns a net.Listener whose Accept yields the connections the sandbox opens to
// that remote address, tunneled back over the existing SSH connection. This is how the
// controller exposes a loopback service (e.g. the egress proxy) to the sandbox without
// the sandbox having any direct network of its own — the sandbox reaches
// 127.0.0.1:<port>, which tunnels here. The caller owns the listener and must Close it.
func (s *SSHRunner) RemoteListen(addr string) (net.Listener, error) {
	return s.client.Listen("tcp", addr)
}

// knownHostsCallback builds a host-key verifier from path, or from the user's
// default ~/.ssh/known_hosts when path is empty. An unknown host is a hard
// error: hosts are never trusted on first sight (no InsecureIgnoreHostKey).
func knownHostsCallback(path string) (ssh.HostKeyCallback, error) {
	path, err := ResolveKnownHosts(path)
	if err != nil {
		return nil, err
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("known_hosts (%s) does not exist — run `iceclimber trust` to record the sandbox's host key first", path)
		}
		return nil, fmt.Errorf("load known_hosts (%s): %w — run `iceclimber trust` to record the host key", path, err)
	}
	return cb, nil
}
