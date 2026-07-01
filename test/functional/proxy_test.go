//go:build functional

package functional

import (
	"bytes"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestProxyPipEgress is the proxy-mode parity anchor: in egress_mode: proxy the sandbox's
// OWN pip reaches real PyPI through Popo's reverse-tunneled MITM proxy (no relay, no
// direct sandbox network), trusting the CA that bootstrap installed — driven only by
// `popo shellenv`. Then it confirms an unlisted host is denied by the egress policy.
func TestProxyPipEgress(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-proxy-" + protocol.NewID()
	scheduleRootCleanup(t, root)
	cfg := writeProxyConfig(t, sb, root, `network:
  allowed_domains:
    - { pattern: "pypi.org", reachable_from: controller }
    - { pattern: "files.pythonhosted.org", reachable_from: controller }`)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")
	runIceclimber(t, "install", "python", "3.12", "--config", cfg, "--transport", "sftp")

	stop := startProxyServe(t, cfg)
	defer stop()

	// The sandbox's own pip, through the proxy, using only the shellenv-provided env.
	py := root + "/runtimes/python/*/bin/python3"
	out := limaSh(t, `eval "$(`+root+`/popo shellenv)" && `+py+
		` -m pip install --disable-pip-version-check --no-cache-dir --target `+root+`/site --no-input six && echo INSTALL_OK`)
	if !strings.Contains(out, "INSTALL_OK") {
		t.Fatalf("pip through the proxy failed:\n%s", out)
	}
	imp := limaSh(t, `PYTHONPATH=`+root+`/site `+py+` -c 'import six; print("IMPORT_OK", six.__version__)'`)
	if !strings.Contains(imp, "IMPORT_OK") {
		t.Errorf("import six from proxy-installed pkg: %s", imp)
	}

	// An unlisted host is refused by the egress policy (403), proving the gate is live.
	den := limaSh(t, `eval "$(`+root+`/popo shellenv)"; wget -qO- https://example.org/ >/dev/null 2>&1 && echo REACHED || echo BLOCKED`)
	if !strings.Contains(den, "BLOCKED") {
		t.Errorf("unlisted host should be blocked by the egress policy, got: %s", den)
	}
}

// TestProxyGitEgress banks the breadth payoff: `git` — a dependency-fetch tool iceclimber
// has NO integration for — works through the proxy with zero new code, purely via the
// bootstrap-installed CA (GIT_SSL_CAINFO) + HTTPS_PROXY. A public clone over HTTPS proves
// the whole "any HTTP(S) tool, no per-tool Go" thesis.
func TestProxyGitEgress(t *testing.T) {
	sb := requireGlibcSandbox(t) // git ships on the glibc (Ubuntu) box
	root := "/tmp/iceclimber-proxygit-" + protocol.NewID()
	scheduleRootCleanupOn(t, sb.Name, root)
	cfg := writeProxyConfigFor(t, sb, root, `network:
  allowed_domains:
    - { pattern: "github.com", reachable_from: controller }`)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")
	stop := startProxyServe(t, cfg)
	defer stop()

	out := limaShOn(t, sb.Name, `eval "$(`+root+`/popo shellenv)"; rm -rf `+root+`/clone; `+
		`git clone --depth 1 https://github.com/octocat/Hello-World.git `+root+`/clone >/dev/null 2>&1 && `+
		`ls `+root+`/clone/README && echo CLONE_OK || echo CLONE_FAIL`)
	if !strings.Contains(out, "CLONE_OK") {
		t.Fatalf("git clone through the proxy failed (a tool with zero iceclimber integration):\n%s", out)
	}
}

// writeProxyConfig writes a proxy-mode config for the musl sandbox.
func writeProxyConfig(t *testing.T, sb sandboxConn, root, extraYAML string) string {
	t.Helper()
	return writeYAML(t, sshConfigYAML(sb)+"remote_root: "+root+"\negress_mode: proxy\n"+strings.TrimRight(extraYAML, "\n")+"\n")
}

// writeProxyConfigFor is writeProxyConfig for an explicitly-named sandbox (glibc).
func writeProxyConfigFor(t *testing.T, sb sandboxConn, root, extraYAML string) string {
	t.Helper()
	return writeProxyConfig(t, sb, root, extraYAML)
}

// startProxyServe runs `iceclimber serve` (proxy mode) in the background with a
// non-tty stdin (so it runs headless — no approver — and config-allowed hosts pass while
// unlisted ones are denied), waits for the proxy to come up, and returns a stop func.
func startProxyServe(t *testing.T, cfg string) func() {
	t.Helper()
	cmd := exec.Command(iceclimberBin, "serve", "--config", cfg, "--transport", "sftp")
	cmd.Stdin = strings.NewReader("") // a pipe (not a char device) → headless, no approver
	var buf syncBuffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "egress proxy up") {
			return func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	t.Fatalf("egress proxy did not come up within 30s:\n%s", buf.String())
	return func() {}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
