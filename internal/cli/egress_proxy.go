package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/proxy"
)

// egressCAPaths are the controller-side CA cert/key for the proxy egress mode, persisted
// per sandbox (alongside approvals/activity) so the sandbox's installed trust survives
// controller restarts.
func egressCAPaths(cfg *config.Config) (certPath, keyPath string) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	dir := filepath.Join(home, ".iceclimber", cfg.SandboxID)
	return filepath.Join(dir, "egress-ca.pem"), filepath.Join(dir, "egress-ca.key")
}

// startEgressProxy, in proxy egress mode, mints/loads the CA, opens the `ssh -R` reverse
// tunnel, and serves the MITM proxy on the sandbox's loopback port for the life of the
// session — so the sandbox's own package managers reach real registries through the
// controller with no direct network. Returns a stop func (called when the serve cycle
// ends; the next reconnect re-establishes) and any startup error. A no-op in relay mode.
//
// The policy/approval/audit wiring lands in PX4; this cut serves allow-all (behind the
// opt-in egress_mode: proxy) and logs each request.
func startEgressProxy(sess *session, cfg *config.Config, out io.Writer) (func(), error) {
	if !cfg.EgressProxy() {
		return func() {}, nil
	}
	certPath, keyPath := egressCAPaths(cfg)
	ca, err := proxy.LoadOrCreateCA(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("egress CA: %w", err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.EgressPort())
	ln, err := sess.runner.RemoteListen(addr)
	if err != nil {
		return nil, fmt.Errorf("egress reverse tunnel on sandbox %s (does the sandbox sshd allow TCP forwarding?): %w", addr, err)
	}
	audit := func(r proxy.Request, v proxy.Verdict, code int) {
		fmt.Fprintf(out, "  egress %d %s %s%s\n", code, r.Method, r.Host, r.Path)
	}
	p := proxy.New(ca, nil /* allow-all until PX4 */, audit, nil)
	go func() { _ = p.Serve(ln) }()
	fmt.Fprintf(out, "  egress proxy up: sandbox 127.0.0.1:%d → controller MITM\n", cfg.EgressPort())
	return func() { _ = ln.Close() }, nil
}

// writeEgressTrust installs, at bootstrap in proxy mode, the CA the sandbox trusts plus
// the per-tool config that routes tools through the proxy and points their TLS trust at
// that CA — all under $ICECLIMBER_HOME, no root. `popo shellenv` (and the agent launcher)
// source egress-env.sh, so an interactive/agent shell picks it up automatically. A no-op
// in relay mode. (Java's truststore needs a JDK, so it's built when Maven runs.)
func writeEgressTrust(ctx context.Context, sess *session, cfg *config.Config) error {
	if !cfg.EgressProxy() {
		return nil
	}
	certPath, keyPath := egressCAPaths(cfg)
	ca, err := proxy.LoadOrCreateCA(certPath, keyPath)
	if err != nil {
		return fmt.Errorf("egress CA: %w", err)
	}
	if err := sess.fs.Mkdir(ctx, path.Join(sess.tree.Root, "certs")); err != nil {
		return err
	}
	if err := sess.fs.WriteFile(ctx, path.Join(sess.tree.Root, "certs", "egress-ca.pem"), ca.CertPEM()); err != nil {
		return fmt.Errorf("write egress CA: %w", err)
	}
	if err := sess.fs.WriteFile(ctx, path.Join(sess.tree.Root, "egress-env.sh"), []byte(egressEnvScript(cfg.EgressPort()))); err != nil {
		return fmt.Errorf("write egress-env.sh: %w", err)
	}
	if err := sess.fs.WriteFile(ctx, path.Join(sess.tree.Root, "maven-settings.xml"), []byte(mavenProxySettings(cfg.EgressPort()))); err != nil {
		return fmt.Errorf("write maven-settings.xml: %w", err)
	}
	return nil
}

// egressEnvScript is the sh block (sourced by popo shellenv / the agent launcher) that
// routes the sandbox's package managers through the proxy and trusts the egress CA. Uses
// $ICECLIMBER_HOME so it stays relocatable; the many env vars cover each ecosystem's own
// TLS-trust knob (OpenSSL/requests/pip/curl/git/cargo and Node's additive store).
func egressEnvScript(port int) string {
	p := fmt.Sprintf("http://127.0.0.1:%d", port)
	return "# iceclimber egress proxy — route package managers through Popo (no direct network)\n" +
		"export HTTPS_PROXY=" + p + "\n" +
		"export https_proxy=$HTTPS_PROXY\n" +
		"export HTTP_PROXY=$HTTPS_PROXY\n" +
		"export http_proxy=$HTTPS_PROXY\n" +
		`CA="$ICECLIMBER_HOME/certs/egress-ca.pem"` + "\n" +
		"export SSL_CERT_FILE=\"$CA\"\n" +
		"export REQUESTS_CA_BUNDLE=\"$CA\"\n" +
		"export PIP_CERT=\"$CA\"\n" +
		"export CURL_CA_BUNDLE=\"$CA\"\n" +
		"export GIT_SSL_CAINFO=\"$CA\"\n" +
		"export CARGO_HTTP_CAINFO=\"$CA\"\n" +
		"export NODE_EXTRA_CA_CERTS=\"$CA\"\n" +
		"export npm_config_https_proxy=$HTTPS_PROXY\n" +
		"export npm_config_proxy=$HTTPS_PROXY\n"
}

// mavenProxySettings is a Maven settings.xml <proxies> block (Maven routes via settings,
// not JVM proxy props). Trust comes from a JDK truststore built when Maven runs.
func mavenProxySettings(port int) string {
	return fmt.Sprintf(`<settings xmlns="http://maven.apache.org/SETTINGS/1.0.0"><proxies><proxy>`+
		`<id>iceclimber-egress</id><active>true</active><protocol>http</protocol><host>127.0.0.1</host><port>%d</port>`+
		`</proxy></proxies></settings>`+"\n", port)
}
