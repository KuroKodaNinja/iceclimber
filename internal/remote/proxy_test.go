package remote

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestHelperProcess is re-executed as the "ProxyCommand" subprocess. It stands in
// for `ssh -W …`: mode "cat" copies stdin→stdout (a bidirectional byte bridge),
// "sleep" blocks until killed, "fail" writes to stderr and exits non-zero.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	mode := ""
	if len(args) > 0 {
		mode = args[0]
	}
	switch mode {
	case "cat":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "sleep":
		select {}
	case "fail":
		fmt.Fprint(os.Stderr, "boom: permission denied")
		os.Exit(3)
	}
	os.Exit(0)
}

func helperArgv(mode string) []string {
	return []string{os.Args[0], "-test.run=^TestHelperProcess$", "--", mode}
}

func TestProxyConn_Roundtrip(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	pc, err := newProxyConn(context.Background(), helperArgv("cat"), "10.0.0.1:22")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	want := "hello over the tunnel\n"
	if _, err := io.WriteString(pc, want); err != nil {
		t.Fatal(err)
	}
	// cat echoes our stdin back on stdout; close write side so the copy can finish.
	_ = pc.stdin.Close()
	got, err := io.ReadAll(pc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("roundtrip = %q, want %q", got, want)
	}

	// RemoteAddr reports the target host:port (tcp) so the knownhosts callback can
	// SplitHostPort it.
	if pc.RemoteAddr().Network() != "tcp" || pc.RemoteAddr().String() != "10.0.0.1:22" {
		t.Errorf("RemoteAddr = %s/%s, want tcp/10.0.0.1:22", pc.RemoteAddr().Network(), pc.RemoteAddr())
	}
}

func TestProxyConn_CloseIdempotent(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	pc, err := newProxyConn(context.Background(), helperArgv("cat"), "10.0.0.1:22")
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := pc.Close(); err != nil { // must not panic or change result
		t.Errorf("second Close: %v", err)
	}
}

func TestProxyConn_ContextCancelReaps(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	ctx, cancel := context.WithCancel(context.Background())
	pc, err := newProxyConn(ctx, helperArgv("sleep"), "10.0.0.1:22")
	if err != nil {
		t.Fatal(err)
	}
	cancel() // kills the blocked subprocess → Read must unblock with an error

	done := make(chan error, 1)
	go func() {
		_, rerr := pc.Read(make([]byte, 8))
		done <- rerr
	}()
	select {
	case rerr := <-done:
		if rerr == nil {
			t.Error("Read returned nil after cancel; want EOF/closed error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not unblock after context cancel")
	}
	if err := pc.Close(); err != nil { // a context-kill is not a Close failure
		t.Errorf("Close after cancel = %v, want nil", err)
	}
}

func TestProxyConn_FailureSurfacesStderr(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	pc, err := newProxyConn(context.Background(), helperArgv("fail"), "10.0.0.1:22")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(pc) // drain to EOF (process exits)
	_ = pc.Close()
	if !strings.Contains(pc.stderrString(), "permission denied") {
		t.Errorf("stderr tail = %q, want it to contain the subprocess error", pc.stderrString())
	}
}
