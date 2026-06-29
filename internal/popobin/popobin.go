// Package popobin embeds the cross-compiled in-sandbox `popo` client binaries and
// hands the right one to bootstrap to relay into a sandbox. The binaries are built
// by `make` (CGO_ENABLED=0, so one per GOARCH runs on musl and glibc) into bin/ and
// embedded; a plain `go build` without that step yields an empty set, and Binary
// returns a clear "build with make" error (bootstrap then warns and the agent falls
// back to the raw file protocol).
package popobin

import (
	"embed"
	"fmt"
)

//go:embed all:bin
var binaries embed.FS

// Binary returns the popo client for a sandbox fingerprint (os like "linux", arch
// like "aarch64"/"x86_64"), mapping to the embedded GOOS/GOARCH build.
func Binary(os, arch string) ([]byte, error) {
	goarch, ok := goarchFor(arch)
	if !ok {
		return nil, fmt.Errorf("no popo client for arch %q", arch)
	}
	name := fmt.Sprintf("bin/popo-%s-%s", os, goarch)
	b, err := binaries.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("no embedded popo client for %s/%s (build with `make`): %w", os, goarch, err)
	}
	return b, nil
}

func goarchFor(arch string) (string, bool) {
	switch arch {
	case "aarch64", "arm64":
		return "arm64", true
	case "x86_64", "amd64":
		return "amd64", true
	default:
		return "", false
	}
}
