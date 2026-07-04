package catalog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/relay"
)

// Resolved is a concrete, checksum-pinned download for one sandbox fingerprint, ready for
// relay.Fetcher.Fetch. The catalog's whole job is turning an Entry + fingerprint into this.
type Resolved struct {
	Entry  Entry
	Asset  relay.Asset // Name (cache key), URL, SHA256 — all filled
	Format string      // unpack.format: "binary" | "tar.gz"
}

// Resolve selects the platform row for (os, arch, libc) and produces the concrete asset:
// its URL, and its SHA256 (inline for the pinned recipe, or fetched from the release/index
// sums file for shasums-file). client fetches the sums file when needed (nil → default).
func (e Entry) Resolve(ctx context.Context, client *http.Client, os, arch, libc string) (Resolved, error) {
	key := platformKey(os, arch, libc)
	p, ok := e.Platforms[key]
	if !ok {
		return Resolved{}, fmt.Errorf("tool %q has no build for %s", e.Name, key)
	}

	url, err := e.assetURL(p)
	if err != nil {
		return Resolved{}, err
	}
	// Cache key: the tool + version + the asset basename, namespaced so two tools' assets
	// (or two versions) never collide in the shared cache.
	cacheName := fmt.Sprintf("tool-%s-%s-%s", e.Name, e.Version, assetBase(p, url))

	sha := p.SHA256
	if e.effectiveRecipe() == recipeShasumsFile {
		if sha, err = e.fetchSum(ctx, client, p); err != nil {
			return Resolved{}, err
		}
	}
	if sha == "" {
		return Resolved{}, fmt.Errorf("tool %q %s: no checksum resolved (recipe %q)", e.Name, key, e.effectiveRecipe())
	}
	return Resolved{
		Entry:  e,
		Asset:  relay.Asset{Name: cacheName, URL: url, SHA256: sha},
		Format: e.Unpack.Format,
	}, nil
}

// assetURL builds the download URL for a platform row per kind: static-pinned carries the
// full URL; github-release composes the release download URL; shasums-index joins base_url.
func (e Entry) assetURL(p Platform) (string, error) {
	switch e.Kind {
	case KindStaticPinned:
		return p.URL, nil
	case KindGithubRelease:
		return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", e.Repo, e.releaseTag(), p.Asset), nil
	case KindShasumsIndex:
		return strings.TrimRight(e.BaseURL, "/") + "/" + p.Asset, nil
	default:
		return "", fmt.Errorf("tool %q: unknown kind %q", e.Name, e.Kind)
	}
}

// releaseTag is the entry's release_tag, defaulting to its version.
func (e Entry) releaseTag() string {
	if e.ReleaseTag != "" {
		return e.ReleaseTag
	}
	return e.Version
}

// assetBase is the asset's own filename (for the cache key): the platform Asset for
// release/index kinds, else the last path segment of the static URL.
func assetBase(p Platform, url string) string {
	if p.Asset != "" {
		return p.Asset
	}
	if i := strings.LastIndexByte(url, '/'); i >= 0 {
		return url[i+1:]
	}
	return url
}

// fetchSum reads the entry's shasums file (from the release / index) and returns the
// sha256 for this platform's asset, matched by basename in the standard two-column
// "<sha256>  <filename>" format.
func (e Entry) fetchSum(ctx context.Context, client *http.Client, p Platform) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	sumsURL, err := e.sumsURL()
	if err != nil {
		return "", err
	}
	body, _, err := relay.HTTPGet(ctx, client, sumsURL)
	if err != nil {
		return "", fmt.Errorf("fetch sums for %s: %w", e.Name, err)
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 4<<20)) // sums files are small; cap defensively
	if err != nil {
		return "", err
	}
	sum := sumFor(string(data), p.Asset)
	if sum == "" {
		return "", fmt.Errorf("tool %q: %s not found in %s", e.Name, p.Asset, e.Checksums.Asset)
	}
	return sum, nil
}

// sumsURL builds the URL of the checksums file (github-release: the release download;
// shasums-index: joined to base_url).
func (e Entry) sumsURL() (string, error) {
	switch e.Kind {
	case KindGithubRelease:
		return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", e.Repo, e.releaseTag(), e.Checksums.Asset), nil
	case KindShasumsIndex:
		return strings.TrimRight(e.BaseURL, "/") + "/" + e.Checksums.Asset, nil
	default:
		return "", fmt.Errorf("tool %q: recipe shasums-file needs a github-release or shasums-index kind", e.Name)
	}
}

// sumFor parses a two-column "<sha256>  <filename>" body and returns the hash for asset
// (matched by exact basename; a leading "*" binary-mode marker is tolerated).
func sumFor(body, asset string) string {
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if name == asset || baseName(name) == asset {
			return fields[0]
		}
	}
	return ""
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
