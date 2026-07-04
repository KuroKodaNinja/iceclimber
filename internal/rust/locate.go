package rust

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Locate returns the absolute bin/cargo path of an installed Rust toolchain matching the
// given version on this platform (highest patch wins; an empty version matches the single/any
// installed toolchain), or an error naming the install command if none is present. rustc sits
// alongside at bin/rustc. Mirrors java.Locate — the install dir is runtimes/rust/<full>-<arch>-<libc>.
func Locate(ctx context.Context, rfs remotefs.FS, root, version, arch, libc string) (string, error) {
	dir := path.Join(root, "runtimes", "rust")
	names, err := rfs.List(ctx, dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", notInstalled(version, arch, libc)
		}
		return "", fmt.Errorf("locate rust: %w", err)
	}

	suffix := "-" + arch + "-" + libc // dir name is "<full>-<arch>-<libc>"
	best, bestV := "", [3]int{-1, -1, -1}
	for _, n := range names {
		if !strings.HasSuffix(n, suffix) {
			continue
		}
		full := strings.TrimSuffix(n, suffix)
		if !versionMatches(full, version) {
			continue
		}
		pv := parseVer(full)
		if best == "" || lessVer(bestV, pv) {
			best, bestV = n, pv
		}
	}
	if best == "" {
		return "", notInstalled(version, arch, libc)
	}
	return path.Join(dir, best, "bin", "cargo"), nil
}

func notInstalled(version, arch, libc string) error {
	v := strings.TrimSpace(version)
	if v == "" {
		v = "stable"
	}
	return fmt.Errorf("rust %s not installed for %s-%s (run: install rust %s)", v, arch, libc, v)
}

// versionMatches reports whether full (a concrete "1.83.0") is in the requested version line
// ("" matches any; "1" matches "1.x.y"; "1.83" matches "1.83.z"; "1.83.0" matches exactly).
func versionMatches(full, requested string) bool {
	r := strings.TrimSpace(requested)
	if r == "" || strings.EqualFold(r, "stable") || strings.EqualFold(r, "latest") {
		return true
	}
	return full == r || strings.HasPrefix(full, r+".")
}

// parseVer extracts the leading major.minor.patch from a Rust version ("1.83.0" → {1,83,0}).
func parseVer(full string) [3]int {
	var v [3]int
	for idx, part := range strings.SplitN(full, ".", 3) {
		if idx >= 3 {
			break
		}
		n := 0
		for _, c := range part {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		v[idx] = n
	}
	return v
}

func lessVer(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
