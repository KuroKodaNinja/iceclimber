package webfetch

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// maxControllerBody caps a controller-venue download (defense against a huge
// body filling memory/disk).
const maxControllerBody = 64 << 20 // 64 MiB

// controllerFetch performs the request from Popo's own network (the controller
// venue) with an SSRF-safe dialer, then materializes the body inline or as a
// blob pushed into the sandbox so Nana can read it.
func controllerFetch(ctx context.Context, fs remotefs.FS, root, method string, req Request, url string) (Result, error) {
	client := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			DialContext:           safeDialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}

	var bodyReader io.Reader
	if req.Body != nil {
		bodyReader = strings.NewReader(*req.Body)
	}
	hreq, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return Result{}, fmt.Errorf("build request: %w", err)
	}
	for k, v := range req.Headers {
		hreq.Header.Set(k, v)
	}

	resp, err := client.Do(hreq)
	if err != nil {
		return Result{}, fmt.Errorf("controller fetch: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxControllerBody))
	if err != nil {
		return Result{}, fmt.Errorf("read body: %w", err)
	}

	enc, inline, blobName, sha := classifyBody(body)
	out := Result{
		StatusCode: resp.StatusCode,
		Headers:    flattenHeaders(resp.Header),
		Venue:      "controller",
		BodySize:   len(body),
		BodySHA256: sha,
	}
	if blobName != "" {
		tree := protocol.Tree{Root: root}
		blobsDir := tree.Blobs() // canonical $ROOT/protocol/blobs (the path NANA.md documents)
		if err := fs.Mkdir(ctx, blobsDir); err != nil {
			return Result{}, fmt.Errorf("ensure blobs dir: %w", err)
		}
		if err := fs.WriteFile(ctx, path.Join(blobsDir, blobName), body); err != nil {
			return Result{}, fmt.Errorf("push blob to sandbox: %w", err)
		}
		out.BodyBlob = tree.BlobRef(blobName) // $ROOT-relative: protocol/blobs/<sha>
	} else {
		out.Encoding, out.BodyInline = enc, inline
	}
	return out, nil
}

// safeDialContext resolves the host and dials only an allowed IP, validated at
// connect time so DNS rebinding can't slip a blocked address past the floor.
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	for _, ipa := range ips {
		if controllerBlockedIP(ipa.IP) {
			continue
		}
		if conn, err := d.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), port)); err == nil {
			return conn, nil
		}
	}
	return nil, fmt.Errorf("refusing to connect to %s: no allowed address", host)
}

// controllerBlockedIP extends the 6a floor (loopback/link-local/metadata) with
// private ranges — Popo sits outside the corporate network, so a controller-venue
// fetch resolving to a private IP is an internal-pivot SSRF.
func controllerBlockedIP(ip net.IP) bool {
	return blockedIP(ip) || ip.IsPrivate()
}

func flattenHeaders(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = strings.Join(v, ", ")
	}
	return out
}
