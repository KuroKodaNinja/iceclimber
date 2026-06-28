package node

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Locate returns the absolute bin/node path of an installed runtime matching the
// given version line on this platform (highest patch wins), or an error naming the
// install command if none is present. npm sits alongside at bin/npm.
func Locate(ctx context.Context, rfs remotefs.FS, root, version, arch, libc string) (string, error) {
	dir := path.Join(root, "runtimes", "node")
	names, err := rfs.List(ctx, dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", notInstalled(version, arch, libc)
		}
		return "", fmt.Errorf("locate node: %w", err)
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
		pv, ok := parseVer(full)
		if !ok {
			continue
		}
		if best == "" || lessVer(bestV, pv) {
			best, bestV = n, pv
		}
	}
	if best == "" {
		return "", notInstalled(version, arch, libc)
	}
	return path.Join(dir, best, "bin", "node"), nil
}

func notInstalled(version, arch, libc string) error {
	return fmt.Errorf("node %s not installed for %s-%s (run: install node %s)", version, arch, libc, version)
}
