package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestResolveKeepAlive pins the interval contract: 0 means the default, a negative
// value disables keepalives, and a positive value passes through unchanged. This is
// what config's ssh.keepalive_interval relies on (the live ping/drop behavior is
// covered by the functional reconnect suite).
func TestResolveKeepAlive(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero defaults to 20s", 0, keepAliveDefault},
		{"negative disables", -1 * time.Second, 0},
		{"positive passes through", 45 * time.Second, 45 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveKeepAlive(c.in); got != c.want {
				t.Errorf("resolveKeepAlive(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// loopbackSSH stands up an SSH client/server over a TCP loopback listener (not
// net.Pipe — its synchronous, unbuffered semantics can deadlock the SSH handshake).
// When respond is false the server receives keepalive global requests but never
// replies — a silently-dead link — so startKeepAlive's miss-count + close-the-client
// path can be exercised.
func loopbackSSH(t *testing.T, respond bool) (*ssh.Client, func()) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	srvCfg := &ssh.ServerConfig{NoClientAuth: true}
	srvCfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		conn, chans, reqs, err := ssh.NewServerConn(nc, srvCfg)
		if err != nil {
			return
		}
		go func() {
			for req := range reqs {
				if respond && req.WantReply {
					_ = req.Reply(true, nil)
				}
				// when !respond: receive but never reply → the client's SendRequest
				// blocks, modelling a dead link.
			}
		}()
		go func() {
			for ch := range chans {
				_ = ch.Reject(ssh.Prohibited, "no channels in this test")
			}
		}()
		_ = conn.Wait()
	}()

	cliCfg := &ssh.ClientConfig{
		User:            "t",
		HostKeyCallback: ssh.FixedHostKey(signer.PublicKey()),
		Timeout:         5 * time.Second,
	}
	nc, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	cc, chans, reqs, err := ssh.NewClientConn(nc, ln.Addr().String(), cliCfg)
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	client := ssh.NewClient(cc, chans, reqs)
	return client, func() { _ = client.Close(); _ = ln.Close() }
}

// TestStartKeepAlive_ClosesUnresponsiveClient: when keepalive pings go unanswered,
// the goroutine closes the client (so the next op fails fast instead of hanging on
// the OS TCP timeout) within roughly interval*keepAliveMaxMiss.
func TestStartKeepAlive_ClosesUnresponsiveClient(t *testing.T) {
	client, cleanup := loopbackSSH(t, false)
	defer cleanup()

	stop := startKeepAlive(client, 20*time.Millisecond)
	defer close(stop)

	closed := make(chan struct{})
	go func() { _ = client.Wait(); close(closed) }()

	select {
	case <-closed: // the goroutine closed the dead client — good
	case <-time.After(3 * time.Second):
		t.Fatal("keepalive did not close an unresponsive client")
	}
}

// TestStartKeepAlive_KeepsResponsiveClient: a server that answers keepalives must NOT
// be closed by the goroutine (no false-positive disconnect).
func TestStartKeepAlive_KeepsResponsiveClient(t *testing.T) {
	client, cleanup := loopbackSSH(t, true)
	defer cleanup()

	stop := startKeepAlive(client, 20*time.Millisecond)
	defer close(stop)

	closed := make(chan struct{})
	go func() { _ = client.Wait(); close(closed) }()

	select {
	case <-closed:
		t.Fatal("keepalive closed a responsive client (false-positive disconnect)")
	case <-time.After(500 * time.Millisecond): // ~25 intervals, all answered
	}
}
