package python

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// extractAndPush opens the cached tarball and streams it into the sandbox.
func (i *Installer) extractAndPush(ctx context.Context, tarball, target string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	return i.pushTarGz(ctx, f, target)
}

// pushTarGz streams a PBS install_only tar.gz into target over the FS. PBS
// entries live under a top-level "python/" which is stripped. Directories are
// created lazily (cached so ExecFS doesn't re-mkdir per file); regular files get
// their tar mode via Chmod (the executable bit is load-bearing); symlinks are
// recreated.
func (i *Installer) pushTarGz(ctx context.Context, r io.Reader, target string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	made := map[string]bool{}
	ensureDir := func(d string) error {
		if d == "" || d == "." || d == "/" || made[d] {
			return nil
		}
		if err := i.cfg.FS.Mkdir(ctx, d); err != nil {
			return err
		}
		made[d] = true
		return nil
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		rel := strings.TrimPrefix(hdr.Name, "python/")
		if rel == "" || rel == "python" {
			continue
		}
		dst := path.Join(target, rel)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := ensureDir(dst); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := ensureDir(path.Dir(dst)); err != nil {
				return err
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				return fmt.Errorf("read %s: %w", hdr.Name, err)
			}
			if err := i.cfg.FS.WriteFile(ctx, dst, data); err != nil {
				return err
			}
			if err := i.cfg.FS.Chmod(ctx, dst, os.FileMode(hdr.Mode).Perm()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := ensureDir(path.Dir(dst)); err != nil {
				return err
			}
			if err := i.cfg.FS.Symlink(ctx, hdr.Linkname, dst); err != nil {
				return err
			}
		default:
			// Skip hardlinks/devices/etc. — PBS install_only doesn't use them.
		}
	}
	return nil
}
