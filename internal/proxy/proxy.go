package proxy

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
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

// Decider judges a request at CONNECT (domain-level; only the host is known then). Full-URL
// package/path granularity is enforced separately, per request, by the PathDenier.
type Decider func(Request) Verdict

// AuditFunc records a serviced request (nil = no audit).
type AuditFunc func(Request, Verdict, int)

// PathDenier is an optional per-request (full-URL) veto applied AFTER the host was
// admitted at CONNECT: it enforces package/path-level deny rules (e.g. block one package
// on an otherwise-allowed registry). Returns (denied, reason). nil = no path-level policy.
type PathDenier func(Request) (bool, string)

// Proxy is the MITM egress proxy: it terminates TLS with CA-minted leaves, consults the
// Decider, forwards allowed requests upstream over real TLS, and audits each one.
type Proxy struct {
	ca       *CA
	decide   Decider
	pathDeny PathDenier
	audit    AuditFunc
	tr       *http.Transport
}

// SetPathDenier installs the per-request path-level veto (see PathDenier).
func (p *Proxy) SetPathDenier(fn PathDenier) { p.pathDeny = fn }

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

// idleReadTimeout bounds how long a connection may sit before its next request line
// arrives (and how long the CONNECT/handshake reads may stall) — a slowloris/goroutine-hold
// cap for the semi-trusted sandbox. It covers idle + header reads, not body streaming.
const idleReadTimeout = 5 * time.Minute

func (p *Proxy) handleConn(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(idleReadTimeout))
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		// Plain HTTP: gate then forward the absolute-URL request.
		req.RequestURI = ""
		_ = c.SetReadDeadline(time.Time{})
		p.forward(c, req, p.decide(requestOf(req)))
		return
	}
	authority := req.Host                  // "host:port" — the upstream target, port preserved
	host := canonHost(hostOnly(authority)) // policy/leaf key: lower-cased, trailing-dot stripped

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
		MinVersion: tls.VersionTLS12,
		// Mint for the ADMITTED CONNECT host, not the client-chosen SNI: SNI is
		// attacker-controlled, so honoring it would let the sandbox drive a fresh RSA keygen
		// per arbitrary name (a CPU lever). Keying on the policy-gated host bounds distinct
		// leaves to admitted hosts, and the forwarded request targets the CONNECT authority
		// anyway, so the leaf name always matches the real destination.
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return p.ca.leafFor(host)
		},
	})
	_ = c.SetReadDeadline(time.Now().Add(idleReadTimeout))
	if err := tlsConn.Handshake(); err != nil {
		return
	}
	defer tlsConn.Close()

	// One MITM'd connection may carry several keep-alive requests (all to the admitted host).
	tbr := bufio.NewReader(tlsConn)
	for {
		_ = c.SetReadDeadline(time.Now().Add(idleReadTimeout)) // bound the idle wait for the next request
		r2, err := http.ReadRequest(tbr)
		if err != nil {
			return
		}
		_ = c.SetReadDeadline(time.Time{}) // unbounded during the body forward / response stream
		r2.URL.Scheme, r2.URL.Host, r2.RequestURI = "https", authority, ""
		if !p.forward(tlsConn, r2, v) {
			return
		}
	}
}

// canonHost canonicalizes a host for policy matching and leaf minting: lower-cased (DNS is
// case-insensitive) with a single trailing dot stripped (fully-qualified form) — so a
// deny/allow rule can't be evaded by casing or a trailing "." the upstream tolerates.
func canonHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// requestOf builds the policy Request from an *http.Request (host canonicalized, no port).
func requestOf(req *http.Request) Request {
	return Request{Method: req.Method, Host: canonHost(hostOnly(req.URL.Host)), Path: req.URL.Path, URL: req.URL.String()}
}

// forward applies the (already-decided) verdict, forwards an allowed request upstream, and
// streams the response back to w. Returns whether the client connection can be reused.
func (p *Proxy) forward(w net.Conn, req *http.Request, v Verdict) bool {
	pr := requestOf(req)
	// Package/path-level veto: the host was admitted at CONNECT, but a per-request rule may
	// block this specific URL (e.g. one package on an allowed registry).
	if v.Allow && p.pathDeny != nil {
		if denied, reason := p.pathDeny(pr); denied {
			v = Verdict{Allow: false, Reason: reason}
		}
	}
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
	defer resp.Body.Close()
	if p.audit != nil {
		p.audit(pr, v, resp.StatusCode)
	}
	return writeResponse(w, req.Method, resp)
}

// writeResponse serializes an upstream response to the client as HTTP/1.1, STREAMING the
// body (no full-body buffering — large artifacts must not sit in controller memory). The
// upstream may have answered over HTTP/2, so we build the status line + framing ourselves
// rather than relying on resp.Write (which would emit an "HTTP/2.0" status line an
// HTTP/1.1 client rejects). The transport already decompressed gzip (we stripped
// Accept-Encoding), so stale content/transfer-encoding headers are dropped and framing is
// chosen from the known length: Content-Length when the upstream gave one, else chunked.
func writeResponse(w net.Conn, method string, resp *http.Response) bool {
	h := resp.Header
	h.Del("Transfer-Encoding")
	h.Del("Content-Encoding")
	h.Del("Connection")
	h.Del("Proxy-Connection")

	if _, err := fmt.Fprintf(w, "HTTP/1.1 %d %s\r\n", resp.StatusCode, http.StatusText(resp.StatusCode)); err != nil {
		return false
	}
	// A HEAD response carries the header a GET WOULD have (including its declared
	// Content-Length) but NO body — Go's transport leaves resp.ContentLength as the declared
	// length while resp.Body is empty, so the normal CL/chunked framing would announce bytes
	// that never come and hang the client (and desync the keep-alive tunnel). Emit headers only.
	if method == http.MethodHead {
		if resp.ContentLength >= 0 {
			h.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
		} else {
			h.Del("Content-Length")
		}
		return writeHeaders(w, h) == nil
	}
	if resp.ContentLength >= 0 && !bodyless(resp.StatusCode) {
		h.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
		if err := writeHeaders(w, h); err != nil {
			return false
		}
		_, err := io.Copy(w, resp.Body)
		return err == nil
	}
	if bodyless(resp.StatusCode) {
		h.Del("Content-Length")
		return writeHeaders(w, h) == nil
	}
	// Unknown length → chunked.
	h.Del("Content-Length")
	h.Set("Transfer-Encoding", "chunked")
	if err := writeHeaders(w, h); err != nil {
		return false
	}
	cw := httputil.NewChunkedWriter(w)
	if _, err := io.Copy(cw, resp.Body); err != nil {
		return false
	}
	if err := cw.Close(); err != nil {
		return false
	}
	_, err := io.WriteString(w, "\r\n") // trailing CRLF after the final chunk
	return err == nil
}

// writeHeaders writes h followed by the blank line ending the header block.
func writeHeaders(w net.Conn, h http.Header) error {
	if err := h.Write(w); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\r\n")
	return err
}

// bodyless reports whether a status code must not carry a response body.
func bodyless(code int) bool {
	return code == http.StatusNoContent || code == http.StatusNotModified || (code >= 100 && code < 200)
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
