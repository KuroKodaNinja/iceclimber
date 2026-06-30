package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHRunner is a Runner backed by a live SSH connection.
type SSHRunner struct {
	client *ssh.Client
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
	// ~/.ssh/config + ProxyJump); false = force a literal direct dial.
	UseSSHConfig *bool
}

// Dial is defined in dial.go (it resolves a dialPlan, then connects directly or
// through a ProxyCommand subprocess before the x/crypto handshake).

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

// Close closes the underlying SSH connection.
func (s *SSHRunner) Close() error {
	return s.client.Close()
}

// NewSFTP opens an SFTP client over the same SSH connection. The caller owns the
// returned client and must Close it. Fails when the server's SFTP subsystem is
// unavailable — the signal to fall back to ExecFS (§6).
func (s *SSHRunner) NewSFTP() (*sftp.Client, error) {
	return sftp.NewClient(s.client)
}

func authMethods(identityFile string) ([]ssh.AuthMethod, error) {
	if identityFile != "" {
		key, err := os.ReadFile(identityFile)
		if err != nil {
			return nil, fmt.Errorf("read identity file %s: %w", identityFile, err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			var passErr *ssh.PassphraseMissingError
			if errors.As(err, &passErr) {
				return nil, fmt.Errorf("identity file %s is passphrase-protected; load it into ssh-agent and clear identity_file", identityFile)
			}
			return nil, fmt.Errorf("parse identity file %s: %w", identityFile, err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}

	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("no identity_file configured and SSH_AUTH_SOCK is unset; cannot authenticate")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("connect to ssh-agent: %w", err)
	}
	ag := agent.NewClient(conn)
	return []ssh.AuthMethod{ssh.PublicKeysCallback(ag.Signers)}, nil
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
