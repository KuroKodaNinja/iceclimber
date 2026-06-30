package node

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Node distribution sources. glibc builds come from nodejs.org/dist; musl builds
// from the unofficial-builds mirror. Both publish an index.json (each version's
// available platform "files") and a per-version SHASUMS256.txt.
const (
	distBaseGlibc = "https://nodejs.org/dist"
	distBaseMusl  = "https://unofficial-builds.nodejs.org/download/release"
)

// resolved is the exact Node asset to install.
type resolved struct {
	FullVersion string // "20.11.1" (no leading v)
	AssetName   string // "node-v20.11.1-linux-arm64-musl.tar.gz"
	URL         string
	SHA256      string
}

// indexEntry is one record from index.json.
type indexEntry struct {
	Version string   `json:"version"` // "v20.11.1"
	Files   []string `json:"files"`   // platform tags, e.g. "linux-arm64-musl"
}

// resolve maps a requested version line ("20" / "20.11") to an exact Node asset
// for this sandbox's platform, with its checksum.
func (i *Installer) resolve(ctx context.Context, requested string) (resolved, error) {
	base, fileTag, err := nodePlatform(i.cfg.OS, i.cfg.Arch, i.cfg.Libc)
	if err != nil {
		return resolved{}, err
	}
	idx, err := i.fetchIndex(ctx, base+"/index.json")
	if err != nil {
		return resolved{}, err
	}
	full, err := pickVersion(idx, requested, fileTag)
	if err != nil {
		return resolved{}, err
	}
	asset := fmt.Sprintf("node-%s-%s.tar.gz", full, fileTag) // full is "v20.11.1"
	url := base + "/" + full + "/" + asset
	sha, err := i.fetchSHA(ctx, base+"/"+full+"/SHASUMS256.txt", asset)
	if err != nil {
		return resolved{}, err
	}
	return resolved{FullVersion: strings.TrimPrefix(full, "v"), AssetName: asset, URL: url, SHA256: sha}, nil
}

// nodePlatform selects the distribution base + the platform "file tag" used in
// both index.json and the asset name (e.g. "linux-arm64-musl").
func nodePlatform(goos, arch, libc string) (base, fileTag string, err error) {
	if goos != "linux" {
		return "", "", fmt.Errorf("unsupported sandbox OS %q (only linux is supported)", goos)
	}
	var a string
	switch arch {
	case "x86_64":
		a = "x64"
	case "aarch64":
		a = "arm64"
	default:
		return "", "", fmt.Errorf("unsupported sandbox arch %q", arch)
	}
	switch libc {
	case "glibc":
		return distBaseGlibc, "linux-" + a, nil
	case "musl":
		return distBaseMusl, "linux-" + a + "-musl", nil
	default:
		return "", "", fmt.Errorf("cannot select a Node build for libc %q; re-run probe to confirm the C library", libc)
	}
}

func (i *Installer) fetchIndex(ctx context.Context, url string) ([]indexEntry, error) {
	body, _, err := i.httpGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch node index: %w", err)
	}
	defer body.Close()
	var idx []indexEntry
	if err := json.NewDecoder(body).Decode(&idx); err != nil {
		return nil, fmt.Errorf("parse node index: %w", err)
	}
	return idx, nil
}

// fetchSHA reads a SHASUMS256.txt body and returns the hash for asset. Lines are
// "<64-hex-hash>  <filename>".
func (i *Installer) fetchSHA(ctx context.Context, url, asset string) (string, error) {
	body, _, err := i.httpGet(ctx, url)
	if err != nil {
		return "", fmt.Errorf("fetch SHASUMS256: %w", err)
	}
	defer body.Close()
	sums, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%s not found in SHASUMS256 (no .tar.gz for this platform?)", asset)
}

// pickVersion returns the highest version (e.g. "v20.11.1") matching the requested
// line that ships fileTag for this platform.
func pickVersion(idx []indexEntry, requested, fileTag string) (string, error) {
	best, bestV := "", [3]int{-1, -1, -1}
	for _, e := range idx {
		v := strings.TrimPrefix(e.Version, "v")
		if !versionMatches(v, requested) || !contains(e.Files, fileTag) {
			continue
		}
		pv, ok := parseVer(v)
		if !ok {
			continue
		}
		if best == "" || lessVer(bestV, pv) {
			best, bestV = e.Version, pv
		}
	}
	if best == "" {
		return "", fmt.Errorf("no Node %s build with %q (this sandbox's platform) in the release index", requested, fileTag)
	}
	return best, nil
}

// versionMatches reports whether v ("20.11.1") satisfies the requested line
// ("20", "20.11", or "20.11.1").
func versionMatches(v, requested string) bool {
	return v == requested || strings.HasPrefix(v, requested+".")
}

func parseVer(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func lessVer(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
