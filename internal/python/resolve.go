package python

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// pbsManifestURL publishes metadata about PBS's latest release (tag +
// asset_url_prefix). The release's SHA256SUMS lists the actual assets + hashes.
const pbsManifestURL = "https://raw.githubusercontent.com/astral-sh/python-build-standalone/latest-release/latest-release.json"

type manifest struct {
	Tag            string `json:"tag"`
	AssetURLPrefix string `json:"asset_url_prefix"`
}

// resolved is the exact PBS asset to install.
type resolved struct {
	FullVersion string // "3.12.13"
	AssetName   string // cpython-3.12.13+20260623-aarch64-unknown-linux-musl-install_only.tar.gz
	URL         string
	SHA256      string
}

// resolve maps a requested minor (e.g. "3.12") to an exact PBS asset for this
// sandbox's platform, with its checksum.
func (i *Installer) resolve(ctx context.Context, minor string) (resolved, error) {
	tr, err := triple(i.cfg.OS, i.cfg.Arch, i.cfg.Libc)
	if err != nil {
		return resolved{}, err
	}
	m, err := i.fetchManifest(ctx)
	if err != nil {
		return resolved{}, err
	}
	body, err := i.httpGet(ctx, m.AssetURLPrefix+"/SHA256SUMS")
	if err != nil {
		return resolved{}, fmt.Errorf("fetch SHA256SUMS: %w", err)
	}
	defer body.Close()
	sums, err := io.ReadAll(body)
	if err != nil {
		return resolved{}, err
	}
	name, sha, full, err := pickAsset(string(sums), minor, m.Tag, tr)
	if err != nil {
		return resolved{}, err
	}
	return resolved{FullVersion: full, AssetName: name, URL: m.AssetURLPrefix + "/" + name, SHA256: sha}, nil
}

func (i *Installer) fetchManifest(ctx context.Context) (manifest, error) {
	body, err := i.httpGet(ctx, pbsManifestURL)
	if err != nil {
		return manifest{}, fmt.Errorf("fetch PBS manifest: %w", err)
	}
	defer body.Close()
	var m manifest
	if err := json.NewDecoder(body).Decode(&m); err != nil {
		return manifest{}, fmt.Errorf("parse PBS manifest: %w", err)
	}
	if m.Tag == "" || m.AssetURLPrefix == "" {
		return manifest{}, fmt.Errorf("PBS manifest missing tag/asset_url_prefix")
	}
	return m, nil
}

// triple builds the PBS platform triple from a probe fingerprint.
func triple(goos, arch, libc string) (string, error) {
	if goos != "linux" {
		return "", fmt.Errorf("unsupported sandbox OS %q (only linux is supported)", goos)
	}
	var libcTag string
	switch libc {
	case "musl":
		libcTag = "musl"
	case "glibc":
		libcTag = "gnu"
	default:
		return "", fmt.Errorf("cannot select a Python build for libc %q; re-run probe to confirm the C library", libc)
	}
	switch arch {
	case "x86_64", "aarch64":
		return fmt.Sprintf("%s-unknown-linux-%s", arch, libcTag), nil
	default:
		return "", fmt.Errorf("unsupported sandbox arch %q", arch)
	}
}

// pickAsset scans a SHA256SUMS body for the highest-patch install_only asset
// matching the minor version, tag, and triple. SHA256SUMS lines are
// "<64-hex-hash>  <filename>".
func pickAsset(sums, minor, tag, triple string) (name, sha, full string, err error) {
	prefix := "cpython-" + minor + "."
	suffix := fmt.Sprintf("+%s-%s-install_only.tar.gz", tag, triple) // excludes "_stripped"
	bestPatch := -1
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		hash, fn := fields[0], fields[1]
		if !strings.HasPrefix(fn, prefix) || !strings.HasSuffix(fn, suffix) {
			continue
		}
		ver := strings.TrimSuffix(strings.TrimPrefix(fn, "cpython-"), suffix) // "<minor>.<patch>"
		patch, perr := strconv.Atoi(ver[strings.LastIndexByte(ver, '.')+1:])
		if perr != nil {
			continue
		}
		if patch > bestPatch {
			bestPatch, name, sha, full = patch, fn, hash, ver
		}
	}
	if bestPatch < 0 {
		return "", "", "", fmt.Errorf("no PBS install_only build for python %s on %s (release %s)", minor, triple, tag)
	}
	return name, sha, full, nil
}
