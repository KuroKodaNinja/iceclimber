//go:build functional

package functional

import (
	"os/exec"
	"strings"
	"testing"
	"time"

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

// TestRawFileProtocol proves the file-I/O-only fallback (PROTOCOL.md): a sandbox-side
// actor talks to Popo with NO popo client — it writes the request envelope to
// outbox/tmp and renames it into outbox/new (atomic delivery), then reads the response
// from inbox/new. We drive exactly those raw file operations from inside the VM via
// the shell, so an agent that can only read/write files (no exec of popo) still works.
func TestRawFileProtocol(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-rawproto-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)
	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	serve := exec.Command(iceclimberBin, "serve", "--yes", "--config", cfg, "--transport", "sftp")
	if err := serve.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	defer func() { _ = serve.Process.Kill(); _, _ = serve.Process.Wait() }()

	// Deliver a ping the raw way: write tmp, then rename into new — no popo binary.
	id := "rawping"
	env := `{"schema_version":1,"id":"` + id + `","type":"ping","created_at":"2026-06-29T00:00:00Z","params":{}}`
	o := root + "/protocol/outbox"
	limaSh(t, "printf '%s' "+remoteQuote(env)+" > "+o+"/tmp/"+id+".json && mv "+o+"/tmp/"+id+".json "+o+"/new/"+id+".json")

	// Read the response from inbox/new the raw way (poll; serve writes it within ~2s).
	resp := root + "/protocol/inbox/new/" + id + ".json"
	var body string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		body = limaSh(t, "cat "+resp+" 2>/dev/null")
		if strings.Contains(body, `"status"`) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !strings.Contains(body, `"status":"ok"`) || !strings.Contains(body, "pong") {
		t.Fatalf("raw file-protocol ping response = %q, want status ok + pong", strings.TrimSpace(body))
	}
}
