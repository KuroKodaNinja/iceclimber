package proxy

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
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
		// Plain HTTP: forward the absolute-URL request and write the response back.
		req.RequestURI = ""
		p.forward(c, req)
		return
	}
	authority := req.Host // "host:port" — the upstream target, port preserved
	host := hostOnly(authority)
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

	// One MITM'd connection may carry several keep-alive requests.
	tbr := bufio.NewReader(tlsConn)
	for {
		r2, err := http.ReadRequest(tbr)
		if err != nil {
			return
		}
		r2.URL.Scheme, r2.URL.Host, r2.RequestURI = "https", authority, ""
		if !p.forward(tlsConn, r2) {
			return
		}
	}
}

// forward applies policy, forwards an allowed request upstream, and writes the response
// back to w. Returns whether the client connection can be reused for another request.
func (p *Proxy) forward(w net.Conn, req *http.Request) bool {
	host := hostOnly(req.URL.Host)
	pr := Request{Method: req.Method, Host: host, Path: req.URL.Path, URL: req.URL.String()}
	v := p.decide(pr)
	if !v.Allow {
		if p.audit != nil {
			p.audit(pr, v, http.StatusForbidden)
		}
		writeStatus(w, http.StatusForbidden, "egress denied: "+v.Reason)
		return true // deny is not a transport error; keep serving the connection
	}
	if v.RewriteHost != "" && v.RewriteHost != host {
		req.URL.Host = v.RewriteHost
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
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if p.audit != nil {
		p.audit(pr, v, resp.StatusCode)
	}

	// Re-serialize to the client as HTTP/1.1 with a definite length — the upstream may
	// have answered over HTTP/2, whose "HTTP/2.0" status line an HTTP/1.1 client rejects.
	resp.Body = io.NopCloser(strings.NewReader(string(body)))
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
