package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// clientThroughProxy builds an http.Client that CONNECTs through p (served on a local
// listener) and trusts the MITM CA — i.e. a stand-in for the sandbox's tooling.
func clientThroughProxy(t *testing.T, p *Proxy) (*http.Client, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go p.Serve(ln)
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(p.ca.CertPEM()) {
		t.Fatal("append CA")
	}
	proxyURL, _ := url.Parse("http://" + ln.Addr().String())
	c := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: caPool}, // trust ONLY the MITM CA
		},
	}
	return c, func() { ln.Close() }
}

// upstreamTLS starts an HTTPS "registry" and returns it plus a transport that trusts its
// self-signed cert (what the proxy uses to reach the real upstream).
func upstreamTLS(t *testing.T, h http.HandlerFunc) (*httptest.Server, *http.Transport) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv, &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: true}
}

func TestProxy_MITM_AllowsAndReachesUpstream(t *testing.T) {
	up, tr := upstreamTLS(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "hello from "+r.Host+r.URL.Path)
	})
	defer up.Close()

	ca, err := NewCA()
	if err != nil {
		t.Fatal(err)
	}
	var seen []Request
	audit := func(r Request, v Verdict, code int) { seen = append(seen, r) }
	p := New(ca, nil, audit, tr) // allow-all
	client, stop := clientThroughProxy(t, p)
	defer stop()

	resp, err := client.Get(up.URL + "/simple/six/")
	if err != nil {
		t.Fatalf("get through proxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "/simple/six/") {
		t.Errorf("through-proxy response = %d %q", resp.StatusCode, body)
	}
	// The audit sees the full request path (package/path-level policy is available).
	sawPath := false
	for _, r := range seen {
		if r.Path == "/simple/six/" {
			sawPath = true
		}
	}
	if !sawPath {
		t.Errorf("audit did not see the full request path; saw %+v", seen)
	}
}

func TestProxy_DeniesByPolicy(t *testing.T) {
	up, tr := upstreamTLS(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "SHOULD NOT REACH") })
	defer up.Close()
	ca, _ := NewCA()

	var saw []Request
	deny := func(r Request) Verdict { return Verdict{Allow: false, Reason: "not on the allowlist"} }
	audit := func(r Request, v Verdict, code int) { saw = append(saw, r) }
	p := New(ca, deny, audit, tr)
	client, stop := clientThroughProxy(t, p)
	defer stop()

	// Denied at CONNECT (before minting a leaf), so the client's tunnel setup fails —
	// no HTTPS response, and the upstream is never reached.
	resp, err := client.Get(up.URL + "/blocked")
	if err == nil {
		resp.Body.Close()
		t.Fatalf("expected the CONNECT to be refused for a denied host, got %d", resp.StatusCode)
	}
	if !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("deny error = %v, want a Forbidden CONNECT refusal", err)
	}
	// The decider was consulted for the host (CONNECT gate; path-level would re-check later).
	if len(saw) != 1 || saw[0].Host != "127.0.0.1" {
		t.Errorf("decider audit = %+v, want one CONNECT-gate check for the host", saw)
	}
}

func TestProxy_HTTP2UpstreamNormalizedTo11(t *testing.T) {
	// httptest TLS server serves HTTP/2 when the client (our proxy transport) supports it.
	up, tr := upstreamTLS(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "proto="+r.Proto)
	})
	defer up.Close()
	ca, _ := NewCA()
	p := New(ca, nil, nil, tr)
	client, stop := clientThroughProxy(t, p)
	defer stop()

	resp, err := client.Get(up.URL + "/x")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	// The client is HTTP/1.1 (it went through the proxy which re-serializes to 1.1).
	if resp.ProtoMajor != 1 {
		t.Errorf("client saw proto %s, want HTTP/1.1 (proxy must normalize)", resp.Proto)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "proto=HTTP/2") {
		t.Logf("upstream served %q (HTTP/2 not negotiated in this env; normalization still exercised)", body)
	}
}

func TestProxy_KeepAliveMultipleRequests(t *testing.T) {
	// Several requests on ONE MITM'd connection (pip fires many): the CONNECT keep-alive
	// loop must serve each. The client transport reuses the tunnel across Gets.
	up, tr := upstreamTLS(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok "+r.URL.Path) })
	defer up.Close()
	ca, _ := NewCA()
	var n int
	p := New(ca, nil, func(Request, Verdict, int) { n++ }, tr)
	client, stop := clientThroughProxy(t, p)
	defer stop()
	for _, path := range []string{"/a", "/b", "/c"} {
		resp, err := client.Get(up.URL + path)
		if err != nil {
			t.Fatalf("request %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), path) {
			t.Errorf("%s → %q", path, body)
		}
	}
	if n != 3 {
		t.Errorf("audited %d requests, want 3 (keep-alive loop served each)", n)
	}
}

func TestCA_LoadOrCreate_Persists(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := dir+"/ca.pem", dir+"/ca.key"
	ca1, err := LoadOrCreateCA(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	ca2, err := LoadOrCreateCA(certPath, keyPath) // reuse, not regenerate
	if err != nil {
		t.Fatal(err)
	}
	if string(ca1.CertPEM()) != string(ca2.CertPEM()) {
		t.Error("LoadOrCreateCA should reuse the persisted CA, not mint a new one")
	}
	// A minted leaf chains to the CA.
	leaf, err := ca1.leafFor("pypi.org")
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca1.CertPEM())
	leafCert, _ := x509.ParseCertificate(leaf.Certificate[0])
	if _, err := leafCert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Errorf("minted leaf does not chain to the CA: %v", err)
	}
}
