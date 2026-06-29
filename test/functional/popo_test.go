//go:build functional

package functional

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestPopoClient proves the in-sandbox popo client end to end: bootstrap relays it
// in, then `popo ping` and `popo python.install 3.12` — run as the agent would, with
// a serving Popo — round-trip through the maildir and print clean results. This is
// the agent's real, deterministic path (no hand-built JSON).
func TestPopoClient(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-popo-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	// bootstrap dropped the client + the docs.
	if out := limaSh(t, "test -x "+root+"/popo && echo ok"); !strings.Contains(out, "ok") {
		t.Fatalf("popo client not installed/executable at %s/popo", root)
	}
	if out := limaSh(t, "test -f "+root+"/skill/PROTOCOL.md && echo ok"); !strings.Contains(out, "ok") {
		t.Errorf("PROTOCOL.md fallback reference not dropped")
	}

	// Serve in the background so the client has someone to talk to.
	serve := exec.Command(iceclimberBin, "serve", "--yes", "--config", cfg, "--transport", "sftp")
	if err := serve.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	defer func() { _ = serve.Process.Kill(); _, _ = serve.Process.Wait() }()

	// `popo ping` — the client builds/delivers/polls/parses; prints a clean line.
	if out := limaSh(t, root+"/popo ping 2>&1"); !strings.Contains(out, "bridge up") {
		t.Errorf("popo ping = %q, want 'bridge up …'", strings.TrimSpace(out))
	}

	// `popo python.install 3.12` — a real install via the client; clean output + the
	// interpreter actually present.
	out := limaSh(t, root+"/popo python.install 3.12 2>&1")
	if !strings.Contains(out, "✓ python.install 3.12.") || !strings.Contains(out, "/runtimes/python/") {
		t.Errorf("popo python.install = %q, want a ✓ line with the path", strings.TrimSpace(out))
	}
	py := strings.TrimSpace(limaSh(t, "ls "+root+"/runtimes/python/*/bin/python3 2>/dev/null | head -1"))
	if py == "" {
		t.Fatal("no python installed after popo python.install")
	}
}
