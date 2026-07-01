package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/egress"
	"github.com/KuroKodaNinja/iceclimber/internal/java"
	"github.com/KuroKodaNinja/iceclimber/internal/proxy"
	"github.com/KuroKodaNinja/iceclimber/internal/webfetch"
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
// Every request is gated through the egress policy at CONNECT (host-level allow/hold/deny +
// persistent approval + rewrite table) and re-checked per request by a package/path-level
// PathDenier; each is audited with its verdict.
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
	ln, err := listenWithRetry(func() (net.Listener, error) { return sess.runner.RemoteListen(addr) }, listenRetryAttempts, time.Sleep)
	if err != nil {
		if isForwardDenied(err) {
			// The port is already forwarded on the sandbox and didn't free up within the retry
			// window — almost always another serve holding it, not a transient restart race.
			return nil, fmt.Errorf("egress reverse tunnel: sandbox port %d is still forwarded after %d tries — another `iceclimber serve` may be holding it for sandbox %q (stop it: pkill -f 'iceclimber.*serve'), or set egress_proxy_port to a free port: %w",
				cfg.EgressPort(), listenRetryAttempts, cfg.SandboxID, err)
		}
		return nil, fmt.Errorf("egress reverse tunnel on sandbox %s (does the sandbox sshd allow TCP forwarding?): %w", addr, err)
	}
	audit := func(r proxy.Request, v proxy.Verdict, code int) {
		tag := ""
		if !v.Allow {
			tag = " DENIED (" + v.Reason + ")"
		} else if v.RewriteHost != "" {
			tag = " → " + v.RewriteHost
		}
		fmt.Fprintf(out, "  egress %d %s %s%s%s\n", code, r.Method, r.Host, r.Path, tag)
	}
	// Gate every request through the egress policy (allow/hold/deny + persistent approval
	// + rewrite table), reusing the interactive approver when serve is supervised.
	pp := newProxyPolicy(sess.policy, sess.approver, cfg.SandboxID)
	// Seed the artifact-deny set from packages already denied at startup (their index is
	// 403'd, so learn-on-serve never sees them) — resolve each denied pip index to its exact
	// artifact URLs, wherever they're hosted. Best-effort; runs off the serve path.
	pp.seedDeniedArtifacts(controllerIndexFetch)
	p := proxy.New(ca, pp.decide, audit, nil)
	// Package/path-level enforcement on an already-admitted host: (1) a deny rule whose glob
	// includes a path blocks just that URL; (2) a denied package's artifact (on a name-less
	// CDN, possibly a different host) is blocked via the resolved/learned artifact set — the
	// host-level CONNECT gate can't see either.
	p.SetPathDenier(func(r proxy.Request) (bool, string) {
		norm := pathDenyURL(r)
		if sess.policy != nil && sess.policy.StoreDenied(norm) {
			return true, "blocked by rule"
		}
		if pp.artifactDenied(norm) {
			return true, "blocked package artifact"
		}
		return false, ""
	})
	// Learn-on-serve: record the artifact URLs of pip indexes served while allowed, so a
	// mid-session deny of that package immediately blocks its already-seen artifacts.
	p.SetResponseObserver(
		func(r proxy.Request) bool { return isPipIndexPath(r.Path) },
		func(r proxy.Request, body []byte) {
			pp.recordIndex(r.URL, egress.ParsePackageIndex(body, indexContentType(r), r.URL))
		},
	)
	go func() { _ = p.Serve(ln) }()
	fmt.Fprintf(out, "  egress proxy up: sandbox 127.0.0.1:%d → controller MITM\n", cfg.EgressPort())
	return func() { _ = ln.Close() }, nil
}

// isPipIndexPath reports whether a request path is a pip "simple" per-package index
// (`…/simple/<pkg>/`) — the responses worth observing to learn a package's artifact URLs.
func isPipIndexPath(p string) bool {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range segs {
		if s == "simple" {
			return i == len(segs)-2 && segs[i+1] != ""
		}
	}
	return false
}

// indexContentType hints ParsePackageIndex toward JSON when the request asked for the PEP 691
// media type (we don't see the response headers here); ParsePackageIndex falls back to HTML.
func indexContentType(proxy.Request) string { return "application/vnd.pypi.simple.v1+json" }

// controllerIndexFetch fetches a package index from the controller's network (used to resolve
// a denied package's artifacts). Short timeout; asks for PEP 691 JSON; TLS validated against
// the controller's real roots. Returns (body, contentType, err).
func controllerIndexFetch(indexURL string) ([]byte, string, error) {
	req, err := http.NewRequest(http.MethodGet, indexURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json, text/html;q=0.5")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("index %s: HTTP %d", indexURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// listenRetryAttempts bounds how many times startEgressProxy re-tries the reverse forward
// when the sandbox reports the port already forwarded — enough (with the backoff below) to
// outlast a just-stopped serve's forward being released (~1-2s), without hanging on a truly
// held port.
const listenRetryAttempts = 5

// listenWithRetry opens the reverse-forward listener, retrying ONLY the "port already
// forwarded" race (a just-stopped serve whose forward the sandbox sshd hasn't released yet)
// with a short linear backoff. Any other error fails fast. sleep is injected for tests.
func listenWithRetry(listen func() (net.Listener, error), attempts int, sleep func(time.Duration)) (net.Listener, error) {
	var err error
	for i := 0; i < attempts; i++ {
		var ln net.Listener
		if ln, err = listen(); err == nil {
			return ln, nil
		}
		if !isForwardDenied(err) {
			return nil, err // not the transient race — don't spin
		}
		if i < attempts-1 {
			sleep(time.Duration(300*(i+1)) * time.Millisecond) // 300ms, 600ms, 900ms, 1.2s
		}
	}
	return nil, err
}

// isForwardDenied reports whether err is the sandbox sshd refusing the reverse forward
// because the port is already forwarded (golang.org/x/crypto/ssh returns a plain error).
func isForwardDenied(err error) bool {
	return err != nil && strings.Contains(err.Error(), "tcpip-forward request denied")
}

// ensureEgressJavaTrust builds the JVM truststore (egress CA) after a JDK install in proxy
// mode, so Maven — and any other JVM tool — validates the proxy's leaves. A no-op in relay
// mode. Idempotent (see java.EnsureEgressTrustStore); javaBin is the just-installed bin/java.
func ensureEgressJavaTrust(ctx context.Context, sess *session, javaBin string) error {
	if !sess.egressProxy {
		return nil
	}
	caPath := path.Join(sess.tree.Root, "certs", "egress-ca.pem")
	storePath := path.Join(sess.tree.Root, "certs", "java-truststore.p12")
	return java.EnsureEgressTrustStore(ctx, sess.runner, javaBin, caPath, storePath)
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
		"export npm_config_proxy=$HTTPS_PROXY\n" +
		// Java: the JVM ignores the OpenSSL/Node knobs above, so trust comes from a PKCS12
		// truststore holding the egress CA (built when a JDK is installed in proxy mode).
		// Guarded on existence so a JVM still starts before that store exists.
		`JAVA_TS="$ICECLIMBER_HOME/certs/java-truststore.p12"` + "\n" +
		`if [ -f "$JAVA_TS" ]; then export JAVA_TOOL_OPTIONS="-Djavax.net.ssl.trustStore=$JAVA_TS -Djavax.net.ssl.trustStorePassword=` + java.EgressTrustStorePass + `${JAVA_TOOL_OPTIONS:+ $JAVA_TOOL_OPTIONS}"; fi` + "\n" +
		// Maven routes through the proxy via settings.xml <proxies> (mvn honors settings, not
		// JVM proxy props); MAVEN_ARGS (Maven 3.9+) injects it as a default arg.
		`[ -f "$ICECLIMBER_HOME/maven-settings.xml" ] && export MAVEN_ARGS="-s $ICECLIMBER_HOME/maven-settings.xml${MAVEN_ARGS:+ $MAVEN_ARGS}"` + "\n"
}

// proxyPolicy gates proxied requests against the egress policy — the same allow/hold/deny
// + persistent-approval + rewrite-table logic web.fetch uses, adapted to a live
// connection. Per-host decisions are memoized (an install fires many requests per host;
// the operator is prompted at most once), and a Hold with no interactive approver denies
// (a live proxy request can't defer to the async pending queue like a maildir fetch can).
type proxyPolicy struct {
	policy    *egress.Policy
	ap        webfetch.Approver // nil in headless serve
	sandboxID string
	mu        sync.Mutex
	cache     map[string]proxy.Verdict
	// H1 close (package artifacts on a name-less CDN): deniedArtifacts is seeded at startup
	// by resolving each denied pip index to its exact artifact URLs; learned records
	// artifactURL→indexURL from indexes served while allowed, so denying that package
	// mid-session blocks its already-seen artifacts. Both keyed by normalizeEgressURL.
	deniedArtifacts map[string]struct{}
	learned         map[string]string
}

func newProxyPolicy(policy *egress.Policy, ap webfetch.Approver, sandboxID string) *proxyPolicy {
	return &proxyPolicy{
		policy: policy, ap: ap, sandboxID: sandboxID,
		cache:           map[string]proxy.Verdict{},
		deniedArtifacts: map[string]struct{}{},
		learned:         map[string]string{},
	}
}

// artifactDenied reports whether a normalized URL is a denied package's artifact — either
// pre-seeded (startup resolve) or learned-on-serve with its index now matching a deny rule.
func (pp *proxyPolicy) artifactDenied(normURL string) bool {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	if _, ok := pp.deniedArtifacts[normURL]; ok {
		return true
	}
	if idx, ok := pp.learned[normURL]; ok && pp.policy != nil && pp.policy.StoreDenied(idx) {
		return true
	}
	return false
}

// recordIndex remembers (artifact → index) for every artifact a served index listed, so a
// later deny of that index blocks the artifacts. indexURL/artifacts are normalized to match
// what the PathDenier checks.
func (pp *proxyPolicy) recordIndex(indexURL string, artifacts []string) {
	idx := normalizeEgressURL(indexURL)
	pp.mu.Lock()
	defer pp.mu.Unlock()
	for _, a := range artifacts {
		pp.learned[normalizeEgressURL(a)] = idx
	}
}

// seedDeniedArtifacts resolves every currently-denied pip index to its artifact URLs (via
// fetch) and pre-populates deniedArtifacts, so a package denied BEFORE serve starts (its
// index is 403'd, never served, so never learned) still has its artifacts blocked. Best
// effort: a fetch/parse failure is skipped (the index-deny still blocks normal installs).
func (pp *proxyPolicy) seedDeniedArtifacts(fetch func(url string) (body []byte, contentType string, err error)) {
	if pp.policy == nil || fetch == nil {
		return
	}
	for _, glob := range pp.policy.Store().Deny() {
		indexURL, ok := egress.IndexURLFromDenyGlob(glob)
		if !ok {
			continue
		}
		body, ct, err := fetch(indexURL)
		if err != nil {
			continue
		}
		pp.mu.Lock()
		for _, a := range egress.ParsePackageIndex(body, ct, indexURL) {
			pp.deniedArtifacts[normalizeEgressURL(a)] = struct{}{}
		}
		pp.mu.Unlock()
	}
}

func (pp *proxyPolicy) decide(r proxy.Request) proxy.Verdict {
	pp.mu.Lock()
	defer pp.mu.Unlock() // serialize decisions (and any prompt); the cache makes this cheap after the first hit per host
	if v, ok := pp.cache[r.Host]; ok {
		return v
	}
	resolved, _, _, err := pp.policy.Resolve(r.URL)
	if err != nil || resolved == "" {
		resolved = r.URL
	}
	rewriteHost := ""
	if h := urlHost(resolved); h != "" && h != r.Host {
		rewriteHost = h
	}
	v := pp.evaluate(r, resolved, rewriteHost)
	pp.cache[r.Host] = v
	return v
}

func (pp *proxyPolicy) evaluate(r proxy.Request, resolved, rewriteHost string) proxy.Verdict {
	// Precedence: an explicit store Deny always wins; then operator-listed allowed_domains
	// pre-allow (so a config allow-list works even under unlisted_domain_policy: deny);
	// then the store/unlisted decision.
	if pp.policy.StoreDenied(resolved) {
		return proxy.Verdict{Allow: false, Reason: "denied by rule"}
	}
	if pp.policy.ConfigAllowed(resolved) {
		return proxy.Verdict{Allow: true, RewriteHost: rewriteHost}
	}
	switch pp.policy.Decide(resolved) {
	case egress.Allow:
		return proxy.Verdict{Allow: true, RewriteHost: rewriteHost}
	case egress.Deny:
		return proxy.Verdict{Allow: false, Reason: "denied by egress policy"}
	default: // Hold
		if pp.ap == nil {
			return proxy.Verdict{Allow: false, Reason: "host not on the allow-list (approve it in an interactive `serve`, or add it to network.allowed_domains)"}
		}
		switch pp.ap.ApproveFetch(context.Background(), webfetch.ApprovalPrompt{
			SandboxID: pp.sandboxID, Method: r.Method, URL: resolved, Host: r.Host,
			Reason: "sandbox egress via the proxy (host not in the allow-list)",
		}) {
		case webfetch.ApproveRemember:
			_ = pp.policy.Store().AddAllow(egress.HostGlob(resolved))
			return proxy.Verdict{Allow: true, RewriteHost: rewriteHost}
		case webfetch.ApproveOnce:
			return proxy.Verdict{Allow: true, RewriteHost: rewriteHost}
		case webfetch.DenyRemember:
			_ = pp.policy.Store().AddDeny(egress.HostGlob(resolved))
			return proxy.Verdict{Allow: false, Reason: "denied by operator"}
		default:
			return proxy.Verdict{Allow: false, Reason: "denied by operator"}
		}
	}
}

// pathDenyURL is the canonical URL the package/path-level deny rules match against.
func pathDenyURL(r proxy.Request) string { return normalizeEgressURL(r.URL) }

// normalizeEgressURL canonicalizes a URL for deny matching, defending against the tricks an
// upstream silently collapses — otherwise a glob like "https://pypi.org/simple/six/*" is
// evaded by "/simple/./six/", "/simple//six/", a %2e-encoded dot, a cased host, or the ":443"
// port. It yields: the real scheme; a lower-cased, port-free, trailing-dot-free host; a
// path.Clean'd path (dot-segments + duplicate slashes collapsed, trailing slash preserved so
// "/six/" still matches "/six/*"); and the query appended. Used both by the request-time
// PathDenier and to normalize index-derived artifact URLs, so the two always agree.
func normalizeEgressURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	clean := path.Clean("/" + strings.TrimPrefix(u.Path, "/"))
	if strings.HasSuffix(u.Path, "/") && !strings.HasSuffix(clean, "/") {
		clean += "/"
	}
	match := scheme + "://" + host + clean
	if u.RawQuery != "" {
		match += "?" + u.RawQuery
	}
	return match
}

// urlHost extracts the hostname from a URL (no port), for rewrite detection.
func urlHost(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return ""
}

// mavenProxySettings is a Maven settings.xml <proxies> block (Maven routes via settings,
// not JVM proxy props). Trust comes from a JDK truststore built when Maven runs.
func mavenProxySettings(port int) string {
	return fmt.Sprintf(`<settings xmlns="http://maven.apache.org/SETTINGS/1.0.0"><proxies><proxy>`+
		`<id>iceclimber-egress</id><active>true</active><protocol>http</protocol><host>127.0.0.1</host><port>%d</port>`+
		`</proxy></proxies></settings>`+"\n", port)
}
