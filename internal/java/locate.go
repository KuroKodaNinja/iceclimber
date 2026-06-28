package java

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Locate returns the absolute bin/java path of an installed JDK matching the given
// feature version on this platform (highest patch wins), or an error naming the
// install command if none is present. javac sits alongside at bin/javac.
func Locate(ctx context.Context, rfs remotefs.FS, root, version, arch, libc string) (string, error) {
	dir := path.Join(root, "runtimes", "java")
	names, err := rfs.List(ctx, dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", notInstalled(version, arch, libc)
		}
		return "", fmt.Errorf("locate java: %w", err)
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
	return path.Join(dir, best, "bin", "java"), nil
}

func notInstalled(version, arch, libc string) error {
	return fmt.Errorf("java %s not installed for %s-%s (run: install java %s)", version, arch, libc, version)
}

// versionMatches reports whether full (semver, maybe with a "+build" suffix) is in
// the requested feature line ("" matches any; "21" matches "21" and "21.x.y").
func versionMatches(full, requested string) bool {
	r := strings.TrimSpace(requested)
	if r == "" {
		return true
	}
	if i := strings.IndexByte(full, '+'); i >= 0 {
		full = full[:i] // compare on major.minor.patch, ignoring the "+build" suffix
	}
	return full == r || strings.HasPrefix(full, r+".")
}

// parseVer extracts the leading major.minor.patch from a semver, ignoring any
// "+build" suffix ("21.0.11+10" → {21,0,11}).
func parseVer(full string) [3]int {
	if i := strings.IndexByte(full, '+'); i >= 0 {
		full = full[:i]
	}
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
