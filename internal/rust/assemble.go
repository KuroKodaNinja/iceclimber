package rust

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

// isWantedComponent selects the toolchain pieces merged into the install prefix — the compiler +
// its target sysroot bits (rustc), the standard library (rust-std-<target>), and cargo. Docs,
// clippy, rustfmt, rls, rust-analyzer, and the llvm tools are dropped to keep the tree lean.
func isWantedComponent(name string) bool {
	return name == "cargo" || name == "rustc" || strings.HasPrefix(name, "rust-std-")
}

// Assemble turns Rust's component-split distribution tarball into a clean strip-1 tree the shared
// push can stream. It rewrites the archive in ONE streaming pass (no on-disk staging): for every
// entry under a wanted component (cargo/, rustc/, rust-std-<target>/) it strips the
// "<topdir>/<component>/" prefix and re-emits it under a single "rust/" top dir — so rustc/bin,
// cargo/bin, and the std's lib/rustlib all land in one prefix where rustc finds its sysroot. Each
// component's manifest.in (a rust-installer bookkeeping file) is dropped. Returns the path to the
// new gzipped tar; the caller/OS temp policy reclaims it.
func Assemble(_ context.Context, tarball string) (string, error) {
	in, err := os.Open(tarball)
	if err != nil {
		return "", err
	}
	defer in.Close()
	gzr, err := gzip.NewReader(in)
	if err != nil {
		return "", fmt.Errorf("gzip: %w", err)
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)

	out, err := os.CreateTemp("", "iceclimber-rust-*.tar.gz")
	if err != nil {
		return "", err
	}
	outName := out.Name()
	gzw := gzip.NewWriter(out)
	tw := tar.NewWriter(gzw)

	fail := func(e error) (string, error) {
		_ = tw.Close()
		_ = gzw.Close()
		_ = out.Close()
		_ = os.Remove(outName)
		return "", e
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail(fmt.Errorf("tar: %w", err))
		}
		// "<topdir>/<component>/<rest>" — keep only the wanted components, dropping the top-dir
		// entry, the component-dir entry, and each component's manifest.in.
		parts := strings.SplitN(hdr.Name, "/", 3)
		if len(parts) < 3 {
			continue
		}
		comp, rest := parts[1], parts[2]
		if !isWantedComponent(comp) || rest == "" || rest == "manifest.in" {
			continue
		}
		nh := &tar.Header{
			Typeflag: hdr.Typeflag,
			Name:     "rust/" + rest,
			Linkname: hdr.Linkname,
			Mode:     hdr.Mode,
			Size:     hdr.Size,
			ModTime:  hdr.ModTime,
		}
		if err := tw.WriteHeader(nh); err != nil {
			return fail(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := io.Copy(tw, tr); err != nil {
				return fail(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		return fail(err)
	}
	if err := gzw.Close(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		return fail(err)
	}
	return outName, nil
}
