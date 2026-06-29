package remote

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// HostKeyError signals that a connection failed host-key verification: the host
// is unknown (not in known_hosts), its recorded key changed, or known_hosts
// could not be loaded. It is the cue to record the key with `iceclimber trust`.
type HostKeyError struct {
	Host     string
	Port     int
	Mismatch bool // true = a different key is already recorded (rotation or MITM)
	err      error
}

func (e *HostKeyError) Error() string {
	what := "host key is not in known_hosts (unknown host)"
	if e.Mismatch {
		what = "recorded host key has CHANGED (key rotation — or a man-in-the-middle)"
	}
	fix := "run `iceclimber trust` to review the fingerprint and record it"
	if e.Mismatch {
		fix = "run `iceclimber trust --replace` only if you expected the key to change"
	}
	return fmt.Sprintf("%s for %s: %s", what, net.JoinHostPort(e.Host, strconv.Itoa(e.portOr22())), fix)
}

func (e *HostKeyError) Unwrap() error { return e.err }

func (e *HostKeyError) portOr22() int {
	if e.Port == 0 {
		return 22
	}
	return e.Port
}

// TrustState is the result of checking a host key against known_hosts.
type TrustState int

const (
	// TrustUnknown: the host has no recorded key.
	TrustUnknown TrustState = iota
	// TrustTrusted: the offered key matches a recorded key.
	TrustTrusted
	// TrustMismatch: a different key is recorded for this host.
	TrustMismatch
)

// ResolveKnownHosts returns path, or the default ~/.ssh/known_hosts when empty.
func ResolveKnownHosts(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, ".ssh", "known_hosts"), nil
}

// Fingerprint is the SHA256 fingerprint of a host key, in the form ssh prints
// (e.g. "SHA256:abc…").
func Fingerprint(key ssh.PublicKey) string { return ssh.FingerprintSHA256(key) }

// errHostKeyCaptured aborts the handshake from the host-key callback once the
// offered key has been captured — FetchHostKey needs only the key, not a full
// (authenticated) connection, so it never has to present credentials.
var errHostKeyCaptured = errors.New("host key captured")

// FetchHostKey dials host:port far enough to learn the key the server offers,
// then aborts the handshake (no authentication is attempted). It is the in-process
// equivalent of `ssh-keyscan`, used by `iceclimber trust` and the console's
// first-connect prompt.
func FetchHostKey(ctx context.Context, cfg DialConfig) (ssh.PublicKey, error) {
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))

	var captured ssh.PublicKey
	clientCfg := &ssh.ClientConfig{
		User: cfg.User,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			captured = key
			return errHostKeyCaptured
		},
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	// The handshake invokes the callback before authentication; the sentinel
	// aborts right after we capture the key, so a successful capture is success
	// regardless of the (expected) handshake error.
	if sshConn, _, _, err := ssh.NewClientConn(conn, addr, clientCfg); err == nil {
		sshConn.Close()
	}
	if captured == nil {
		return nil, fmt.Errorf("no host key offered by %s", addr)
	}
	return captured, nil
}

// CheckHostKey reports whether key is trusted, unknown, or a mismatch against the
// known_hosts file at knownHostsPath (resolving the default when empty). A missing
// known_hosts file means the host is simply unknown.
func CheckHostKey(knownHostsPath, host string, port int, key ssh.PublicKey) (TrustState, error) {
	path, err := ResolveKnownHosts(knownHostsPath)
	if err != nil {
		return TrustUnknown, err
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return TrustUnknown, nil
		}
		return TrustUnknown, fmt.Errorf("load known_hosts (%s): %w", path, err)
	}
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	verr := cb(addr, &net.TCPAddr{IP: net.IPv4zero, Port: port}, key)
	if verr == nil {
		return TrustTrusted, nil
	}
	var ke *knownhosts.KeyError
	if errors.As(verr, &ke) {
		if len(ke.Want) > 0 {
			return TrustMismatch, nil
		}
		return TrustUnknown, nil
	}
	return TrustUnknown, fmt.Errorf("verify host key: %w", verr)
}

// RecordHostKey appends key for host:port to the known_hosts file (resolving the
// default when empty), creating the file and its directory if needed. When replace
// is true, any existing plaintext entries for the host are removed first — the path
// for an ephemeral host that reused an address with a new key. Hashed entries are
// left untouched (they can't be matched without the salt; use `ssh-keygen -R`).
func RecordHostKey(knownHostsPath, host string, port int, key ssh.PublicKey, replace bool) error {
	path, err := ResolveKnownHosts(knownHostsPath)
	if err != nil {
		return err
	}
	if port == 0 {
		port = 22
	}
	token := knownhosts.Normalize(net.JoinHostPort(host, strconv.Itoa(port)))

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create known_hosts dir: %w", err)
	}

	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read known_hosts (%s): %w", path, err)
	}

	var kept []string
	if len(existing) > 0 {
		for _, line := range strings.Split(strings.TrimRight(string(existing), "\n"), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if replace && matchesHost(line, token) {
				continue // drop a stale entry for this host
			}
			kept = append(kept, line)
		}
	}

	kept = append(kept, knownhosts.Line([]string{token}, key))
	out := strings.Join(kept, "\n") + "\n"
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		return fmt.Errorf("write known_hosts (%s): %w", path, err)
	}
	return nil
}

// matchesHost reports whether a known_hosts line names token among its hosts.
// Hashed entries (|1|…) never match — they're preserved.
func matchesHost(line, token string) bool {
	_, hosts, _, _, _, err := ssh.ParseKnownHosts([]byte(line))
	if err != nil {
		return false
	}
	for _, h := range hosts {
		if h == token {
			return true
		}
	}
	return false
}
