//go:build functional

package functional

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestSingleWriterLocks proves the per-sandbox single-writer enforcement end to end:
// a second serving process fails fast naming the first (the fleet-critical double-dispatch
// guard), while a mutating one-shot during an active serve is ALLOWED with a notice —
// proxy-mode flows (maven build --tier proxy) depend on that coexistence. Both processes
// share one private HOME, since the locks live under ~/.iceclimber/<sandbox>/.
func TestSingleWriterLocks(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-lock-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)
	scheduleRootCleanup(t, root)
	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	home := t.TempDir()
	env := append(os.Environ(), "HOME="+home)

	// First serving process: headless serve under the private HOME.
	serve := exec.Command(iceclimberBin, "serve", "--yes", "--config", cfg, "--transport", "sftp")
	serve.Env = env
	var serveOut strings.Builder
	serve.Stdout, serve.Stderr = &serveOut, &serveOut
	if err := serve.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	t.Cleanup(func() { _ = serve.Process.Kill(); _, _ = serve.Process.Wait() })
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(serveOut.String(), "serving sandbox") {
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(serveOut.String(), "serving sandbox") {
		t.Fatalf("serve did not come up:\n%s", serveOut.String())
	}

	// A SECOND serving process must fail fast, naming the holder — before any dial.
	second := exec.Command(iceclimberBin, "serve", "--yes", "--config", cfg, "--transport", "sftp")
	second.Env = env
	out, err := second.CombinedOutput()
	if err == nil {
		t.Fatalf("second serve should be refused while the first holds the lock:\n%s", out)
	}
	for _, want := range []string{"already being served", "pid"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("second serve's refusal missing %q:\n%s", want, out)
		}
	}

	// A mutating one-shot during the active serve is ALLOWED (with a notice on stderr).
	install := exec.Command(iceclimberBin, "install", "shell", "--config", cfg, "--transport", "sftp")
	install.Env = env
	iout, err := install.CombinedOutput()
	if err != nil {
		t.Fatalf("install during serve must be allowed (proxy-mode flows depend on it): %v\n%s", err, iout)
	}
	if !strings.Contains(string(iout), "being served") {
		t.Errorf("install during serve should print the serving notice:\n%s", iout)
	}

	// TWO concurrent one-shots contend: hold the ops lock via a slow install? Cheap proxy:
	// re-run install (idempotent, fast) sequentially to prove the lock RELEASES cleanly.
	again := exec.Command(iceclimberBin, "install", "shell", "--config", cfg, "--transport", "sftp")
	again.Env = env
	if aout, err := again.CombinedOutput(); err != nil || !strings.Contains(string(aout), "already installed") {
		t.Errorf("sequential re-install should succeed after lock release: %v\n%s", err, aout)
	}
}
