package cli

import (
	"fmt"
	"io"
	"os"
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
