package python

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Locate returns the absolute bin/python3 path of an installed runtime matching
// the given minor version on this platform (highest patch wins), or an error
// naming the install command if none is present.
func Locate(ctx context.Context, rfs remotefs.FS, root, minor, arch, libc string) (string, error) {
	dir := path.Join(root, "runtimes", "python")
	names, err := rfs.List(ctx, dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", notInstalled(minor, arch, libc)
		}
		return "", fmt.Errorf("locate python: %w", err)
	}

	suffix := "-" + arch + "-" + libc // dir name is "<full>-<arch>-<libc>"
	prefix := minor + "."
	best, bestPatch := "", -1
	for _, n := range names {
		if !strings.HasSuffix(n, suffix) {
			continue
		}
		full := strings.TrimSuffix(n, suffix)
		if !strings.HasPrefix(full, prefix) {
			continue
		}
		patch, err := strconv.Atoi(full[strings.LastIndexByte(full, '.')+1:])
		if err != nil {
			continue
		}
		if patch > bestPatch {
			best, bestPatch = n, patch
		}
	}
	if best == "" {
		return "", notInstalled(minor, arch, libc)
	}
	return path.Join(dir, best, "bin", "python3"), nil
}

func notInstalled(minor, arch, libc string) error {
	return fmt.Errorf("python %s not installed for %s-%s (run: install python %s)", minor, arch, libc, minor)
}
