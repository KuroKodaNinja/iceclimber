package java

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

// extractAndPush opens the cached tarball and streams it into target.
func (i *Installer) extractAndPush(ctx context.Context, tarball, target string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	return i.pushTarGz(ctx, f, target)
}

// pushTarGz streams a JDK tar.gz into target over the FS. Adoptium tarballs nest
// everything under one top-level directory (e.g. "jdk-21.0.11+10/"); we detect and
// strip it from the first entry so we don't depend on its exact name. Directories
// are created lazily (cached so ExecFS doesn't re-mkdir per file); regular files
// get their tar mode via Chmod (the executable bit on bin/java, bin/javac is
// load-bearing); symlinks are recreated.
func (i *Installer) pushTarGz(ctx context.Context, r io.Reader, target string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	strip := ""
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
		if strip == "" {
			if idx := strings.IndexByte(hdr.Name, '/'); idx >= 0 {
				strip = hdr.Name[:idx+1] // top-level dir + "/"
			}
		}
		rel := strings.TrimPrefix(hdr.Name, strip)
		if rel == "" {
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
			// Skip hardlinks/devices/etc.
		}
	}
	return nil
}
