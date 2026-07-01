//go:build functional

package functional

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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

// TestProxyNpmEgress is the npm parity anchor: in egress_mode: proxy the sandbox's OWN npm
// (bundled with the node runtime iceclimber installs) reaches the real npm registry through
// Popo's reverse-tunneled MITM proxy, trusting the bootstrap-installed CA and routed via
// npm_config_proxy/NODE_EXTRA_CA_CERTS — all from `popo shellenv`, no relay, no per-tool Go.
func TestProxyNpmEgress(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-proxynpm-" + protocol.NewID()
	scheduleRootCleanup(t, root)
	cfg := writeProxyConfig(t, sb, root, `network:
  allowed_domains:
    - { pattern: "registry.npmjs.org", reachable_from: controller }`)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")
	runIceclimber(t, "install", "node", "24", "--config", cfg, "--transport", "sftp")

	stop := startProxyServe(t, cfg)
	defer stop()

	// The sandbox's own npm, through the proxy, installing a pure-JS package into a prefix.
	// npm is a `#!/usr/bin/env node` script, so node must be on PATH to run it.
	node := root + "/runtimes/node/*/bin/node"
	nodeDir := `$(dirname $(ls ` + node + `))`
	out := limaSh(t, `eval "$(`+root+`/popo shellenv)"; export PATH="`+nodeDir+`:$PATH"; `+
		`npm install --prefix `+root+`/npmapp --no-audit --no-fund left-pad >/dev/null 2>&1 && echo INSTALL_OK || echo INSTALL_FAIL`)
	if !strings.Contains(out, "INSTALL_OK") {
		t.Fatalf("npm through the proxy failed:\n%s", out)
	}
	imp := limaSh(t, `NODE_PATH=`+root+`/npmapp/node_modules `+node+` -e 'console.log("IMPORT_"+require("left-pad")("x",3))'`)
	if !strings.Contains(imp, "IMPORT_") {
		t.Errorf("require left-pad from proxy-installed pkg: %s", imp)
	}
}

// TestProxyCondaEgress is the conda parity anchor: in egress_mode: proxy the glibc sandbox's
// OWN native conda reaches conda-forge (conda.anaconda.org) through the proxy — a real solve
// + download, no relay/repodata synthesis — trusting the CA via conda's OpenSSL knobs
// (SSL_CERT_FILE/REQUESTS_CA_BUNDLE), all from `popo shellenv`.
func TestProxyCondaEgress(t *testing.T) {
	sb := requireGlibcSandbox(t) // native conda ships on the glibc box
	requireSandboxConda(t, sb)
	root := "/tmp/iceclimber-proxyconda-" + protocol.NewID()
	scheduleRootCleanupOn(t, sb.Name, root)
	cfg := writeProxyConfigFor(t, sb, root, `network:
  allowed_domains:
    - { pattern: "conda.anaconda.org", reachable_from: controller }`)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")
	stop := startProxyServe(t, cfg)
	defer stop()

	// A tiny noarch package (no python dependency) — proves the native solve+download reaches
	// conda-forge through the proxy without a multi-minute python env build.
	env := root + "/condaenv"
	out := limaShOn(t, sb.Name, `eval "$(`+root+`/popo shellenv)"; `+
		`conda create -y -p `+env+` -c conda-forge --override-channels tzdata >/dev/null 2>&1 && echo CREATE_OK || echo CREATE_FAIL`)
	if !strings.Contains(out, "CREATE_OK") {
		t.Fatalf("conda create through the proxy failed:\n%s", out)
	}
	meta := limaShOn(t, sb.Name, `ls `+env+`/conda-meta/tzdata-*.json 2>/dev/null && echo META_OK || echo META_MISSING`)
	if !strings.Contains(meta, "META_OK") {
		t.Errorf("conda env missing the proxy-installed package: %s", meta)
	}
}

// TestProxyPackagePathDeny proves package/path-level egress policy: on an ALLOWED registry
// host, a deny rule that includes a path ("https://pypi.org/simple/six/*") blocks just that
// one package (the proxy's per-request path veto returns 403) while every other package on
// the same host still installs. The domain-level relay never saw paths; the MITM proxy does.
func TestProxyPackagePathDeny(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-proxypath-" + protocol.NewID()
	scheduleRootCleanup(t, root)
	approvals := filepath.Join(t.TempDir(), "approvals.json")
	if err := os.WriteFile(approvals, []byte(`{"allow":null,"deny":["https://pypi.org/simple/six/*"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := writeProxyConfig(t, sb, root, `approvals_file: `+approvals+`
network:
  allowed_domains:
    - { pattern: "pypi.org", reachable_from: controller }
    - { pattern: "files.pythonhosted.org", reachable_from: controller }`)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")
	runIceclimber(t, "install", "python", "3.12", "--config", cfg, "--transport", "sftp")

	stop := startProxyServe(t, cfg)
	defer stop()

	py := root + "/runtimes/python/*/bin/python3"
	// six is on an allowed host but matched by the path-deny rule → pip can't resolve it.
	six := limaSh(t, `eval "$(`+root+`/popo shellenv)"; `+py+
		` -m pip install --disable-pip-version-check --no-cache-dir --target `+root+`/site --no-input six `+
		`>/dev/null 2>&1 && echo SIX_INSTALLED || echo SIX_BLOCKED`)
	if !strings.Contains(six, "SIX_BLOCKED") {
		t.Errorf("six should be blocked by the package-path deny rule, got: %s", six)
	}
	// idna, same host, no matching rule → installs normally (proves it's path- not host-level).
	idna := limaSh(t, `eval "$(`+root+`/popo shellenv)"; `+py+
		` -m pip install --disable-pip-version-check --no-cache-dir --target `+root+`/site --no-input idna `+
		`>/dev/null 2>&1 && echo IDNA_INSTALLED || echo IDNA_FAILED`)
	if !strings.Contains(idna, "IDNA_INSTALLED") {
		t.Fatalf("idna (same host, not denied) should install through the proxy, got: %s", idna)
	}
	imp := limaSh(t, `PYTHONPATH=`+root+`/site `+py+` -c 'import idna; print("IMPORT_OK", idna.__version__)'`)
	if !strings.Contains(imp, "IMPORT_OK") {
		t.Errorf("import idna from proxy-installed pkg: %s", imp)
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
