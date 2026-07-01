package proxy

import (
	"bytes"
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

func TestProxy_StreamsChunkedAndLargeBody(t *testing.T) {
	// An upstream that flushes without a Content-Length → the proxy must chunk it back to
	// the client, streaming (not buffering) a large body. This is the case that hung when
	// re-serializing via resp.Write; explicit framing must handle it.
	const chunks, size = 32, 64 * 1024 // 2 MiB total
	up, tr := upstreamTLS(t, func(w http.ResponseWriter, r *http.Request) {
		fl, _ := w.(http.Flusher)
		buf := bytes.Repeat([]byte("x"), size)
		for i := 0; i < chunks; i++ {
			w.Write(buf)
			if fl != nil {
				fl.Flush()
			}
		}
	})
	defer up.Close()
	ca, _ := NewCA()
	p := New(ca, nil, nil, tr)
	client, stop := clientThroughProxy(t, p)
	defer stop()

	resp, err := client.Get(up.URL + "/big")
	if err != nil {
		t.Fatalf("get big body through proxy: %v", err)
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		t.Fatalf("read big body: %v", err)
	}
	if n != int64(chunks*size) {
		t.Errorf("streamed %d bytes, want %d", n, chunks*size)
	}
}

func TestProxy_PathDenier(t *testing.T) {
	// The host is admitted (allow-all decider), but a per-request path veto blocks one
	// package path — that request gets a 403 response (the CONNECT already succeeded),
	// while other paths on the same host pass. Package/path-level policy.
	up, tr := upstreamTLS(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "reached "+r.URL.Path) })
	defer up.Close()
	ca, _ := NewCA()
	p := New(ca, nil, nil, tr) // host allow-all
	p.SetPathDenier(func(r Request) (bool, string) {
		return strings.Contains(r.Path, "/blocked/"), "package blocked"
	})
	client, stop := clientThroughProxy(t, p)
	defer stop()

	// An allowed path passes.
	ok, err := client.Get(up.URL + "/simple/six/")
	if err != nil {
		t.Fatalf("allowed path: %v", err)
	}
	ok.Body.Close()
	if ok.StatusCode != 200 {
		t.Errorf("allowed path status = %d, want 200", ok.StatusCode)
	}
	// A blocked path is 403 (a response, since the host/CONNECT was admitted).
	bad, err := client.Get(up.URL + "/blocked/evil/")
	if err != nil {
		t.Fatalf("blocked path: %v", err)
	}
	body, _ := io.ReadAll(bad.Body)
	bad.Body.Close()
	if bad.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "package blocked") {
		t.Errorf("blocked path = %d %q, want 403 + reason", bad.StatusCode, body)
	}
}

func TestProxy_HeadRequest(t *testing.T) {
	// A HEAD carries the header a GET would (its declared Content-Length) but NO body. The
	// proxy must not announce those bytes and then send none — that hangs the client and
	// desyncs the keep-alive tunnel for the next request.
	up, tr := upstreamTLS(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		if r.Method == http.MethodHead {
			return // headers only
		}
		w.Write(bytes.Repeat([]byte("y"), 1024))
	})
	defer up.Close()
	ca, _ := NewCA()
	p := New(ca, nil, nil, tr)
	client, stop := clientThroughProxy(t, p)
	defer stop()

	resp, err := client.Head(up.URL + "/pkg")
	if err != nil {
		t.Fatalf("head through proxy (hang/desync?): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.ContentLength != 1024 {
		t.Errorf("head = %d, content-length %d; want 200 / 1024", resp.StatusCode, resp.ContentLength)
	}
	// The tunnel must remain usable — a follow-up GET on the reused transport still works.
	g, err := client.Get(up.URL + "/pkg")
	if err != nil {
		t.Fatalf("get after head (tunnel desynced): %v", err)
	}
	n, _ := io.Copy(io.Discard, g.Body)
	g.Body.Close()
	if n != 1024 {
		t.Errorf("get after head read %d bytes, want 1024", n)
	}
}

func TestProxy_ContentLengthZero(t *testing.T) {
	// An explicit Content-Length: 0 on a non-bodyless status must be framed as zero bytes
	// (not chunked, no hang) and leave the tunnel reusable.
	up, tr := upstreamTLS(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(200)
	})
	defer up.Close()
	ca, _ := NewCA()
	p := New(ca, nil, nil, tr)
	client, stop := clientThroughProxy(t, p)
	defer stop()
	resp, err := client.Get(up.URL + "/empty")
	if err != nil {
		t.Fatalf("get cl:0: %v", err)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || n != 0 {
		t.Errorf("cl:0 response = %d, %d bytes; want 200 / 0", resp.StatusCode, n)
	}
	if _, err := client.Get(up.URL + "/again"); err != nil {
		t.Errorf("second request after cl:0 failed: %v", err)
	}
}

func TestProxy_PathDenyDrainsBodyKeepsTunnel(t *testing.T) {
	// A path-denied request WITH a body: the proxy must drain the request body before the
	// 403 so the next keep-alive request on the same connection parses cleanly.
	up, tr := upstreamTLS(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "reached "+r.URL.Path) })
	defer up.Close()
	ca, _ := NewCA()
	p := New(ca, nil, nil, tr)
	p.SetPathDenier(func(r Request) (bool, string) { return strings.Contains(r.Path, "/blocked/"), "blocked" })
	client, stop := clientThroughProxy(t, p)
	defer stop()

	bad, err := client.Post(up.URL+"/blocked/x", "text/plain", strings.NewReader(strings.Repeat("z", 8192)))
	if err != nil {
		t.Fatalf("post to denied path: %v", err)
	}
	body, _ := io.ReadAll(bad.Body)
	bad.Body.Close()
	if bad.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "blocked") {
		t.Errorf("denied POST = %d %q, want 403 + reason", bad.StatusCode, body)
	}
	// Same tunnel, next request must parse (body was drained).
	ok, err := client.Get(up.URL + "/ok")
	if err != nil {
		t.Fatalf("get after denied POST (tunnel desynced by undrained body): %v", err)
	}
	g, _ := io.ReadAll(ok.Body)
	ok.Body.Close()
	if ok.StatusCode != 200 || !strings.Contains(string(g), "/ok") {
		t.Errorf("request after denied POST = %d %q", ok.StatusCode, g)
	}
}

func TestProxy_ResponseObserver(t *testing.T) {
	// The observer sees the FULL body of an index-path 200 and the client still gets it intact;
	// a non-index path is not observed (streams).
	up, tr := upstreamTLS(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "BODY:"+r.URL.Path)
	})
	defer up.Close()
	ca, _ := NewCA()
	p := New(ca, nil, nil, tr)
	var observed []string
	p.SetResponseObserver(
		func(r Request) bool { return strings.HasPrefix(r.Path, "/simple/") },
		func(r Request, body []byte) { observed = append(observed, r.Path+"="+string(body)) },
	)
	client, stop := clientThroughProxy(t, p)
	defer stop()

	// Observed path: body delivered intact AND handed to observe.
	resp, err := client.Get(up.URL + "/simple/six/")
	if err != nil {
		t.Fatalf("get observed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "BODY:/simple/six/" {
		t.Errorf("observed response body = %q, want it intact", body)
	}
	if len(observed) != 1 || observed[0] != "/simple/six/=BODY:/simple/six/" {
		t.Errorf("observe saw %v, want the full index body", observed)
	}

	// Non-observed path: still works, not recorded.
	r2, err := client.Get(up.URL + "/packages/x.whl")
	if err != nil {
		t.Fatalf("get non-observed: %v", err)
	}
	r2.Body.Close()
	if len(observed) != 1 {
		t.Errorf("a non-index path must not be observed; observed=%v", observed)
	}
}

func TestCanonHost(t *testing.T) {
	for in, want := range map[string]string{
		"PyPI.org":  "pypi.org",
		"pypi.org.": "pypi.org",
		"Pypi.ORG.": "pypi.org",
		"pypi.org":  "pypi.org",
	} {
		if got := canonHost(in); got != want {
			t.Errorf("canonHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProxy_NoContentStatus(t *testing.T) {
	// A 204 must be serialized bodyless (no Content-Length, no hang waiting for a body).
	up, tr := upstreamTLS(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	defer up.Close()
	ca, _ := NewCA()
	p := New(ca, nil, nil, tr)
	client, stop := clientThroughProxy(t, p)
	defer stop()
	resp, err := client.Get(up.URL + "/empty")
	if err != nil {
		t.Fatalf("get 204: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
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
