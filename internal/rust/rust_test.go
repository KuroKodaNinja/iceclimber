package rust

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/runtime"
)

func TestRustTarget(t *testing.T) {
	tests := []struct {
		fp      runtime.Fingerprint
		want    string
		wantErr bool
	}{
		{runtime.Fingerprint{OS: "linux", Arch: "aarch64", Libc: "musl"}, "aarch64-unknown-linux-musl", false},
		{runtime.Fingerprint{OS: "linux", Arch: "x86_64", Libc: "glibc"}, "x86_64-unknown-linux-gnu", false},
		{runtime.Fingerprint{OS: "linux", Arch: "aarch64", Libc: "glibc"}, "aarch64-unknown-linux-gnu", false},
		{runtime.Fingerprint{OS: "darwin", Arch: "aarch64", Libc: "musl"}, "", true}, // linux only
		{runtime.Fingerprint{OS: "linux", Arch: "riscv64", Libc: "musl"}, "", true},  // unsupported arch
		{runtime.Fingerprint{OS: "linux", Arch: "aarch64", Libc: "unknown"}, "", true},
	}
	for _, tt := range tests {
		got, err := rustTarget(tt.fp)
		if tt.wantErr {
			if err == nil {
				t.Errorf("rustTarget(%+v) = %q, want error", tt.fp, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("rustTarget(%+v) = %q,%v; want %q", tt.fp, got, err, tt.want)
		}
	}
}

func TestFirstQuotedToken(t *testing.T) {
	if v := firstQuotedToken(`version = "1.83.0 (90b35a623 2024-11-26)"`); v != "1.83.0" {
		t.Errorf("firstQuotedToken = %q, want 1.83.0", v)
	}
	if v := firstQuotedToken(`version = "1.83.0"`); v != "1.83.0" {
		t.Errorf("firstQuotedToken (no space) = %q, want 1.83.0", v)
	}
	if v := firstQuotedToken(`no quotes here`); v != "" {
		t.Errorf("firstQuotedToken (no quote) = %q, want empty", v)
	}
}

// TestAssemble is the core of the engine's assemble hook: a component-split rust-shaped tarball
// must be rewritten to a single "rust/"-wrapped prefix that keeps cargo/rustc/rust-std, merges
// their trees, and drops manifest.in + the unwanted components + the installer bookkeeping.
func TestAssemble(t *testing.T) {
	target := "aarch64-unknown-linux-musl"
	top := "rust-1.83.0-" + target + "/"
	src := buildRustTarball(t, top, target)

	// Write the synthetic source tarball to a temp file (Assemble takes a path).
	in, err := os.CreateTemp(t.TempDir(), "src-*.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Write(src); err != nil {
		t.Fatal(err)
	}
	in.Close()

	outPath, err := Assemble(context.Background(), in.Name())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	defer os.Remove(outPath)

	got := tarEntries(t, outPath)
	want := map[string]bool{
		"rust/bin/cargo": true,
		"rust/bin/rustc": true,
		"rust/lib/rustlib/" + target + "/lib/libstd.rlib": true,
	}
	for w := range want {
		if !got[w] {
			t.Errorf("assembled tree missing %q (got %v)", w, keys(got))
		}
	}
	// Dropped: manifest.in, the unwanted component (clippy), and the top-level installer files.
	for _, bad := range []string{
		"rust/manifest.in", "rust/bin/cargo-clippy", "rust/install.sh", top + "cargo/bin/cargo",
	} {
		if got[bad] {
			t.Errorf("assembled tree should not contain %q", bad)
		}
	}
}

// buildRustTarball synthesizes a component-split rust distribution tarball: cargo + rustc +
// rust-std (kept), clippy-preview (dropped), each with a manifest.in, plus top-level install.sh.
func buildRustTarball(t *testing.T, top, target string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	reg := func(name, body string) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	reg(top+"install.sh", "#!/bin/sh\n")
	reg(top+"cargo/manifest.in", "file:bin/cargo\n")
	reg(top+"cargo/bin/cargo", "CARGO")
	reg(top+"rustc/manifest.in", "file:bin/rustc\n")
	reg(top+"rustc/bin/rustc", "RUSTC")
	reg(top+"rust-std-"+target+"/manifest.in", "x\n")
	reg(top+"rust-std-"+target+"/lib/rustlib/"+target+"/lib/libstd.rlib", "STD")
	reg(top+"clippy-preview/manifest.in", "x\n")
	reg(top+"clippy-preview/bin/cargo-clippy", "CLIPPY")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tarEntries(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	out := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = true
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
