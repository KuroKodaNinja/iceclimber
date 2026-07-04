package java

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/relay"
	"github.com/KuroKodaNinja/iceclimber/internal/runtime"
)

// Adoptium (Eclipse Temurin) publishes a queryable assets API with per-build
// SHA256 checksums — the analog of Python's PBS metadata. musl builds are the
// "alpine-linux" os; glibc builds are "linux".
const adoptiumAPI = "https://api.adoptium.net/v3"

// adoptiumAsset is one element of the /v3/assets/latest response.
type adoptiumAsset struct {
	Binary struct {
		Architecture string `json:"architecture"`
		ImageType    string `json:"image_type"`
		OS           string `json:"os"`
		Package      struct {
			Checksum string `json:"checksum"`
			Link     string `json:"link"`
			Name     string `json:"name"`
		} `json:"package"`
	} `json:"binary"`
	Version struct {
		Semver string `json:"semver"`
	} `json:"version"`
}

// Resolve maps a requested feature version ("21" / "17") to the latest Temurin JDK asset for
// this sandbox's platform, with its checksum, via the Adoptium API. It's the Spec's live
// upstream resolver: the engine handles the operator-pin short-circuit before this is called.
func Resolve(ctx context.Context, fp runtime.Fingerprint, client *http.Client, feature string) (relay.Pin, error) {
	arch, apiOS, err := adoptiumPlatform(fp.OS, fp.Arch, fp.Libc)
	if err != nil {
		return relay.Pin{}, err
	}
	feature = strings.TrimSpace(feature)
	url := fmt.Sprintf("%s/assets/latest/%s/hotspot?architecture=%s&image_type=jdk&os=%s",
		adoptiumAPI, feature, arch, apiOS)
	body, _, err := relay.HTTPGet(ctx, client, url)
	if err != nil {
		return relay.Pin{}, fmt.Errorf("query Adoptium API: %w", err)
	}
	defer body.Close()

	var assets []adoptiumAsset
	if err := json.NewDecoder(body).Decode(&assets); err != nil {
		return relay.Pin{}, fmt.Errorf("parse Adoptium response: %w", err)
	}
	r, err := selectAsset(assets, arch, apiOS)
	if err != nil {
		return relay.Pin{}, fmt.Errorf("Temurin JDK %s for %s/%s (%s): %w", feature, fp.Arch, fp.Libc, apiOS, err)
	}
	return r, nil
}

// selectAsset picks the matching JDK .tar.gz (with a checksum) from an Adoptium
// response. Pure so it can be unit-tested without the network.
func selectAsset(assets []adoptiumAsset, arch, apiOS string) (relay.Pin, error) {
	for _, a := range assets {
		if a.Binary.ImageType != "jdk" || a.Binary.Architecture != arch || a.Binary.OS != apiOS {
			continue
		}
		if !strings.HasSuffix(a.Binary.Package.Link, ".tar.gz") {
			continue
		}
		if a.Binary.Package.Checksum == "" {
			return relay.Pin{}, fmt.Errorf("asset %s lacks a checksum", a.Binary.Package.Name)
		}
		return relay.Pin{
			FullVersion: a.Version.Semver,
			AssetName:   a.Binary.Package.Name,
			URL:         a.Binary.Package.Link,
			SHA256:      a.Binary.Package.Checksum,
		}, nil
	}
	return relay.Pin{}, fmt.Errorf("no matching .tar.gz build")
}

// adoptiumPlatform maps the probe fingerprint to Adoptium's architecture + os
// names ("x64"/"aarch64", "linux" for glibc / "alpine-linux" for musl).
func adoptiumPlatform(goos, arch, libc string) (apiArch, apiOS string, err error) {
	if goos != "linux" {
		return "", "", fmt.Errorf("unsupported sandbox OS %q (only linux is supported)", goos)
	}
	switch arch {
	case "x86_64":
		apiArch = "x64"
	case "aarch64":
		apiArch = "aarch64"
	default:
		return "", "", fmt.Errorf("unsupported sandbox arch %q", arch)
	}
	switch libc {
	case "glibc":
		return apiArch, "linux", nil
	case "musl":
		return apiArch, "alpine-linux", nil
	default:
		return "", "", fmt.Errorf("cannot select a JDK build for libc %q; re-run probe to confirm the C library", libc)
	}
}
