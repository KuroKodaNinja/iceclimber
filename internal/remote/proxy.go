package remote

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"
)

// proxyConn is a net.Conn backed by a ProxyCommand subprocess: we Write to the
// process's stdin (→ the remote) and Read from its stdout (← the remote). The SSH
// transport (ssh.NewClientConn) then runs over it to reach a target *through a
// jumpbox*, with OpenSSH owning the bastion connection. The subprocess's stderr
// is mirrored to our stderr so any bastion password/2FA/host-key prompt is visible
// — OpenSSH reads those secrets from /dev/tty, never from our stdin pipe, so the
// data channel is never corrupted — and a bounded tail is kept to enrich errors.
type proxyConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *boundedBuffer
	cancel context.CancelFunc

	waitErr   chan error
	closeOnce sync.Once
	closeErr  error
}

// newProxyConn starts argv and returns a net.Conn over its stdio. ctx cancellation
// kills (and reaps) the process, which unblocks any in-flight Read/Write — that is
// how a target-handshake timeout propagates through the tunnel.
func newProxyConn(ctx context.Context, argv []string) (*proxyConn, error) {
	if len(argv) == 0 {
		return nil, errors.New("proxy: empty argv")
	}
	cctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	// cmd.Env left nil → inherits this process's environment, so SSH_AUTH_SOCK and
	// TERM reach the bastion ssh (it authenticates as the operator's own ssh would).
	// No agent forwarding is configured or needed.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr := &boundedBuffer{max: 8 << 10}
	cmd.Stderr = io.MultiWriter(os.Stderr, stderr)
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	pc := &proxyConn{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr, cancel: cancel, waitErr: make(chan error, 1)}
	go func() { pc.waitErr <- cmd.Wait() }()
	return pc, nil
}

func (c *proxyConn) Read(b []byte) (int, error)  { return c.stdout.Read(b) }
func (c *proxyConn) Write(b []byte) (int, error) { return c.stdin.Write(b) }

// Close is idempotent: close stdin (EOF to the remote — a `-W` ssh then exits),
// cancel (kill as a backstop), and reap the process so it's not left a zombie.
func (c *proxyConn) Close() error {
	c.closeOnce.Do(func() {
		cerr := c.stdin.Close()
		c.cancel()
		werr := <-c.waitErr // reap; context kill surfaces as an *exec.ExitError, ignored
		_ = c.stdout.Close()
		if cerr != nil {
			c.closeErr = cerr
		} else if werr != nil && !isKilled(werr) {
			c.closeErr = werr
		}
	})
	return c.closeErr
}

// stderrString returns the captured tail of the subprocess's stderr — used to
// enrich a handshake error (e.g. the bastion's "Permission denied").
func (c *proxyConn) stderrString() string { return c.stderr.String() }

func (c *proxyConn) LocalAddr() net.Addr  { return proxyAddr("ssh-proxy-local") }
func (c *proxyConn) RemoteAddr() net.Addr { return proxyAddr("ssh-proxy") }

// Deadlines are best-effort no-ops: OS pipes can't honor them and ssh.NewClientConn
// doesn't set them — connection-level timeouts are driven by the context instead.
func (c *proxyConn) SetDeadline(time.Time) error      { return nil }
func (c *proxyConn) SetReadDeadline(time.Time) error  { return nil }
func (c *proxyConn) SetWriteDeadline(time.Time) error { return nil }

type proxyAddr string

func (proxyAddr) Network() string  { return "ssh-proxy" }
func (a proxyAddr) String() string { return string(a) }

// isKilled reports whether err is the expected result of our own context-kill
// (so Close doesn't report it as a failure).
func isKilled(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee)
}

// boundedBuffer is a thread-safe writer that retains only the last max bytes —
// enough to surface a subprocess's final error lines without unbounded growth.
type boundedBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
