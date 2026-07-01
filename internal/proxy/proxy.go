package proxy

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Request is the per-request context the Decider judges. Host is the target host (no
// port); Path is the request path. URL is the full https URL for logging/rewrite.
type Request struct {
	Method string
	Host   string
	Path   string
	URL    string
}

// Verdict is the policy decision for a request. Allow gates it; RewriteHost (optional)
// redirects the request to a different upstream host (e.g. an internal mirror), preserving
// the path — the domain-level rewrite table. Reason is surfaced on a deny.
type Verdict struct {
	Allow       bool
	RewriteHost string
	Reason      string
}

// Decider judges a request. The full URL is available, so a decider MAY gate at
// package/path granularity — though v1 policy is domain-level (see PX4).
type Decider func(Request) Verdict

// AuditFunc records a serviced request (nil = no audit).
type AuditFunc func(Request, Verdict, int)

// Proxy is the MITM egress proxy: it terminates TLS with CA-minted leaves, consults the
// Decider, forwards allowed requests upstream over real TLS, and audits each one.
type Proxy struct {
	ca     *CA
	decide Decider
	audit  AuditFunc
	tr     *http.Transport
}

// New builds a proxy. A nil decider allows everything; a nil audit is silent. upstream
// may override the upstream transport (tests inject a root pool); nil uses a default that
// validates upstreams against the controller's real root store.
func New(ca *CA, decide Decider, audit AuditFunc, upstream *http.Transport) *Proxy {
	if decide == nil {
		decide = func(Request) Verdict { return Verdict{Allow: true} }
	}
	if upstream == nil {
		upstream = &http.Transport{
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			IdleConnTimeout:       90 * time.Second,
		}
	}
	return &Proxy{ca: ca, decide: decide, audit: audit, tr: upstream}
}

// Serve accepts connections (typically from the reverse-tunnel listener) until ln closes.
func (p *Proxy) Serve(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go p.handleConn(c)
	}
}

// bufConn lets TLS/HTTP read bytes already buffered after the CONNECT line (the pitfall
// that broke a naive http.Server+Hijack) while writing to the raw conn.
type bufConn struct {
	r *bufio.Reader
	net.Conn
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func (p *Proxy) handleConn(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		// Plain HTTP: gate then forward the absolute-URL request.
		req.RequestURI = ""
		p.forward(c, req, p.decide(requestOf(req)))
		return
	}
	authority := req.Host // "host:port" — the upstream target, port preserved
	host := hostOnly(authority)

	// Gate at CONNECT — BEFORE minting a leaf — so the untrusted sandbox can't force
	// unbounded RSA keygen for hosts it can't reach anyway (domain-level policy needs only
	// the host; path-level would re-check per request). The verdict is reused for every
	// request on this connection.
	pr := Request{Method: req.Method, Host: host, Path: "", URL: "https://" + host + "/"}
	v := p.decide(pr)
	if !v.Allow {
		if p.audit != nil {
			p.audit(pr, v, http.StatusForbidden)
		}
		writeStatus(c, http.StatusForbidden, "egress denied: "+v.Reason)
		return
	}
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	tlsConn := tls.Server(&bufConn{br, c}, &tls.Config{
		GetCertificate: func(hi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hi.ServerName
			if name == "" {
				name = host // clients send no SNI for IP-addressed hosts; use the CONNECT target
			}
			return p.ca.leafFor(name)
		},
	})
	if err := tlsConn.Handshake(); err != nil {
		return
	}
	defer tlsConn.Close()

	// One MITM'd connection may carry several keep-alive requests (all to the admitted host).
	tbr := bufio.NewReader(tlsConn)
	for {
		r2, err := http.ReadRequest(tbr)
		if err != nil {
			return
		}
		r2.URL.Scheme, r2.URL.Host, r2.RequestURI = "https", authority, ""
		if !p.forward(tlsConn, r2, v) {
			return
		}
	}
}

// requestOf builds the policy Request from an *http.Request (host without port).
func requestOf(req *http.Request) Request {
	return Request{Method: req.Method, Host: hostOnly(req.URL.Host), Path: req.URL.Path, URL: req.URL.String()}
}

// forward applies the (already-decided) verdict, forwards an allowed request upstream, and
// streams the response back to w. Returns whether the client connection can be reused.
func (p *Proxy) forward(w net.Conn, req *http.Request, v Verdict) bool {
	pr := requestOf(req)
	if !v.Allow {
		if p.audit != nil {
			p.audit(pr, v, http.StatusForbidden)
		}
		// Drain the body so the next keep-alive request on this connection parses cleanly.
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
		writeStatus(w, http.StatusForbidden, "egress denied: "+v.Reason)
		return true // deny is not a transport error; keep serving the connection
	}
	if v.RewriteHost != "" && v.RewriteHost != pr.Host {
		// Redirect to the rewrite target, preserving any non-default port.
		if _, port, err := net.SplitHostPort(req.URL.Host); err == nil {
			req.URL.Host = net.JoinHostPort(v.RewriteHost, port)
		} else {
			req.URL.Host = v.RewriteHost
		}
		req.Host = v.RewriteHost
	}
	stripHopByHop(req.Header)
	// Let our transport negotiate gzip and decompress transparently — forwarding a
	// compressed body verbatim risks a framing desync (a classic MITM hazard).
	req.Header.Del("Accept-Encoding")

	resp, err := p.tr.RoundTrip(req)
	if err != nil {
		if p.audit != nil {
			p.audit(pr, v, http.StatusBadGateway)
		}
		writeStatus(w, http.StatusBadGateway, "upstream error")
		return false
	}
	body, rerr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if rerr != nil {
		if p.audit != nil {
			p.audit(pr, v, http.StatusBadGateway)
		}
		writeStatus(w, http.StatusBadGateway, "upstream read error")
		return false
	}
	if p.audit != nil {
		p.audit(pr, v, resp.StatusCode)
	}

	// Re-serialize to the client as HTTP/1.1 with a definite length: the upstream may have
	// answered over HTTP/2 (whose "HTTP/2.0" status line an HTTP/1.1 client rejects) or
	// chunked, and buffering lets us present a clean Content-Length the tunneled client
	// frames unambiguously. (Full-body buffering is a known limitation for very large
	// artifacts — relay mode is the path for those; streaming here is future work.)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", fmt.Sprint(len(body)))
	resp.TransferEncoding = nil
	resp.Proto, resp.ProtoMajor, resp.ProtoMinor = "HTTP/1.1", 1, 1
	resp.Close = false
	return resp.Write(w) == nil
}

func stripHopByHop(h http.Header) {
	for _, k := range []string{"Proxy-Connection", "Connection", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(k)
	}
}

func writeStatus(w io.Writer, code int, msg string) {
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Length: %d\r\nContent-Type: text/plain\r\nConnection: keep-alive\r\n\r\n%s",
		code, http.StatusText(code), len(msg), msg)
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}
