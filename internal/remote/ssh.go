package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHRunner is a Runner backed by a live SSH connection.
type SSHRunner struct {
	client *ssh.Client
}

// DialConfig is the minimal connection input for Dial.
type DialConfig struct {
	Host         string
	Port         int
	User         string
	IdentityFile string // optional; falls back to ssh-agent when empty
	KnownHosts   string // optional; defaults to ~/.ssh/known_hosts
}

// Dial opens an SSH connection to the sandbox host. Host keys are verified
// against the user's known_hosts file: an unknown host is a hard error rather
// than silently trusted (no InsecureIgnoreHostKey).
func Dial(ctx context.Context, cfg DialConfig) (*SSHRunner, error) {
	auth, err := authMethods(cfg.IdentityFile)
	if err != nil {
		return nil, err
	}
	hostKeyCallback, err := knownHostsCallback(cfg.KnownHosts)
	if err != nil {
		return nil, err
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	clientCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback,
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh handshake %s: %w", addr, err)
	}
	return &SSHRunner{client: ssh.NewClient(sshConn, chans, reqs)}, nil
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
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("locate home dir: %w", err)
		}
		path = filepath.Join(home, ".ssh", "known_hosts")
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts (%s): %w — connect once with plain ssh (or ssh-keyscan) to record the host key first", path, err)
	}
	return cb, nil
}
