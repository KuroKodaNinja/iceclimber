package remotefs

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

// TreePusher is an optional FS capability: extract a plain, already
// prefix-stripped tar stream into target in one shot. ExecFS implements it via
// `tar` over the exec channel, so a runtime tree pushes in a single round-trip
// instead of one `cat` per file — the ExecFS bulk-transfer path (plan §6).
type TreePusher interface {
	PushTar(ctx context.Context, tarStream io.Reader, target string) error
}

// PushTarGz extracts an upstream runtime archive into target. The archive is a
// .tar.gz nested under a single top-level directory (e.g. "python/", "node-v.../",
// "jdk-.../"), which is stripped. When fs implements TreePusher (the exec
// transport) it re-packs a prefix-stripped tar and streams it for a single-exec
// extract; otherwise it writes each entry over the FS (the SFTP path). Regular
// files keep their tar mode (the executable bit is load-bearing); symlinks are
// recreated; directories are created lazily.
//
// Centralizing this here means the per-language installers share one push path and
// automatically get the bulk transfer on SFTP-less sandboxes.
func PushTarGz(ctx context.Context, fs FS, gz io.Reader, target string) error {
	if tp, ok := fs.(TreePusher); ok {
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(restrip(gz, pw)) }()
		return tp.PushTar(ctx, pr, target)
	}
	return pushPerFile(ctx, fs, gz, target)
}

// stripLead drops the first path component ("a/b/c" → "b/c"); a bare top-level
// entry ("a" or "a/") yields "".
func stripLead(name string) string {
	name = strings.TrimPrefix(name, "./")
	i := strings.IndexByte(name, '/')
	if i < 0 {
		return ""
	}
	return name[i+1:]
}

// restrip gunzips gz and writes a plain tar to w with the top-level component
// stripped from every entry — the stream handed to a remote `tar -x`.
func restrip(gz io.Reader, w io.Writer) error {
	zr, err := gzip.NewReader(gz)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	tw := tar.NewWriter(w)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		rel := stripLead(hdr.Name)
		if rel == "" {
			continue
		}
		out := &tar.Header{
			Typeflag: hdr.Typeflag,
			Name:     rel,
			Linkname: hdr.Linkname,
			Mode:     hdr.Mode,
			Size:     hdr.Size,
			ModTime:  hdr.ModTime,
		}
		if err := tw.WriteHeader(out); err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := io.Copy(tw, tr); err != nil {
				return err
			}
		}
	}
	return tw.Close()
}

// pushPerFile extracts gz entry-by-entry over the FS — the fallback when the FS is
// not a TreePusher (i.e. SFTP, where per-file writes are already efficient).
func pushPerFile(ctx context.Context, fs FS, gz io.Reader, target string) error {
	zr, err := gzip.NewReader(gz)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)

	made := map[string]bool{}
	ensureDir := func(d string) error {
		if d == "" || d == "." || d == "/" || made[d] {
			return nil
		}
		if err := fs.Mkdir(ctx, d); err != nil {
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
		rel := stripLead(hdr.Name)
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
			if err := fs.WriteFile(ctx, dst, data); err != nil {
				return err
			}
			if err := fs.Chmod(ctx, dst, os.FileMode(hdr.Mode).Perm()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := ensureDir(path.Dir(dst)); err != nil {
				return err
			}
			if err := fs.Symlink(ctx, hdr.Linkname, dst); err != nil {
				return err
			}
		default:
			// Skip hardlinks/devices/etc.
		}
	}
	return nil
}
