// Package rust installs a relocatable Rust toolchain (rustc + cargo + the standard library)
// into the sandbox, the fourth managed language and the first to need the engine's assemble
// hook: Rust ships a component-split distribution tarball (cargo/, rustc/, rust-std-<target>/
// each a separate subtree plus an install.sh), not a ready strip-1 bin/ layout, so it's merged
// into one prefix on the controller before being pushed (see assemble.go). Like the other
// runtimes it is relay-based — Popo downloads the official static.rust-lang.org build on its own
// network, sha256-verifies it, assembles it locally, and pushes the tree over a remotefs.FS.
package rust

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/pins"
	"github.com/KuroKodaNinja/iceclimber/internal/relay"
	"github.com/KuroKodaNinja/iceclimber/internal/runtime"
)

// distBase is pinned centrally in internal/pins; stableChannel derives from it.
var (
	distBase      = pins.Rt("rust").DistBase
	stableChannel = distBase + "/channel-rust-stable.toml"
)

// Resolve maps a requested version ("stable"/"latest"/"" → the current stable, or an exact
// "1.83.0") to the combined Rust toolchain tarball for this sandbox's platform, with its sha256.
func Resolve(ctx context.Context, fp runtime.Fingerprint, client *http.Client, version string) (relay.Pin, error) {
	target, err := rustTarget(fp)
	if err != nil {
		return relay.Pin{}, err
	}
	ver := strings.TrimSpace(version)
	if ver == "" || strings.EqualFold(ver, "stable") || strings.EqualFold(ver, "latest") {
		ver, err = stableVersion(ctx, client)
		if err != nil {
			return relay.Pin{}, err
		}
	}
	asset := fmt.Sprintf("rust-%s-%s.tar.gz", ver, target)
	url := distBase + "/" + asset
	sha, err := fetchSHA256(ctx, client, url+".sha256")
	if err != nil {
		return relay.Pin{}, fmt.Errorf("rust %s for %s: %w", ver, target, err)
	}
	return relay.Pin{FullVersion: ver, AssetName: asset, URL: url, SHA256: sha}, nil
}

// rustTarget builds the Rust target triple from a probe fingerprint (musl → "musl", glibc →
// "gnu"). The static.rust-lang.org host builds for these targets run natively on the sandbox.
func rustTarget(fp runtime.Fingerprint) (string, error) {
	if fp.OS != "linux" {
		return "", fmt.Errorf("unsupported sandbox OS %q (only linux is supported)", fp.OS)
	}
	var libcTag string
	switch fp.Libc {
	case "musl":
		libcTag = "musl"
	case "glibc":
		libcTag = "gnu"
	default:
		return "", fmt.Errorf("cannot select a Rust build for libc %q; re-run probe to confirm the C library", fp.Libc)
	}
	switch fp.Arch {
	case "x86_64", "aarch64":
		return fmt.Sprintf("%s-unknown-linux-%s", fp.Arch, libcTag), nil
	default:
		return "", fmt.Errorf("unsupported sandbox arch %q", fp.Arch)
	}
}

// stableVersion reads the concrete stable version (e.g. "1.83.0") from the rust channel
// manifest's [pkg.rust] version field ("1.83.0 (<hash> <date>)" → "1.83.0").
func stableVersion(ctx context.Context, client *http.Client) (string, error) {
	body, _, err := relay.HTTPGet(ctx, client, stableChannel)
	if err != nil {
		return "", fmt.Errorf("fetch rust stable channel: %w", err)
	}
	defer body.Close()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	inRust := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[pkg.") {
			inRust = line == "[pkg.rust]"
			continue
		}
		if inRust && strings.HasPrefix(line, "version") {
			if v := firstQuotedToken(line); v != "" {
				return v, nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("rust stable channel manifest missing [pkg.rust] version")
}

// firstQuotedToken pulls the first whitespace-delimited token out of the first quoted string on
// a line: `version = "1.83.0 (abc 2024-01-01)"` → "1.83.0".
func firstQuotedToken(line string) string {
	q := strings.IndexByte(line, '"')
	if q < 0 {
		return ""
	}
	rest := line[q+1:]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	v := rest[:end]
	if sp := strings.IndexByte(v, ' '); sp >= 0 {
		v = v[:sp]
	}
	return v
}

// fetchSHA256 reads a rust ".sha256" sidecar ("<64-hex>  <filename>") and returns the hash.
func fetchSHA256(ctx context.Context, client *http.Client, url string) (string, error) {
	body, _, err := relay.HTTPGet(ctx, client, url)
	if err != nil {
		return "", err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 4096))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 || len(fields[0]) != 64 {
		return "", fmt.Errorf("malformed .sha256 body for %s", url)
	}
	return fields[0], nil
}
