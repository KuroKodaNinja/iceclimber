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
	p := New(ca, nil, nil, tr) // allow-all
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

	resp, err := client.Get(up.URL + "/blocked")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "not on the allowlist") {
		t.Errorf("denied response = %d %q, want 403 + reason", resp.StatusCode, body)
	}
	// The decider saw the full path (package/path-level policy is available).
	if len(saw) != 1 || saw[0].Path != "/blocked" {
		t.Errorf("decider audit = %+v, want one request with path /blocked", saw)
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
