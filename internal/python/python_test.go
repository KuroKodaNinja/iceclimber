package python

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

func TestTriple(t *testing.T) {
	tests := []struct {
		os, arch, libc string
		want           string
		wantErr        bool
	}{
		{"linux", "aarch64", "musl", "aarch64-unknown-linux-musl", false},
		{"linux", "x86_64", "glibc", "x86_64-unknown-linux-gnu", false},
		{"linux", "aarch64", "glibc", "aarch64-unknown-linux-gnu", false},
		{"darwin", "aarch64", "musl", "", true},   // only linux sandboxes
		{"linux", "riscv64", "musl", "", true},    // unsupported arch
		{"linux", "aarch64", "unknown", "", true}, // low-confidence libc
	}
	for _, tt := range tests {
		got, err := triple(tt.os, tt.arch, tt.libc)
		if tt.wantErr {
			if err == nil {
				t.Errorf("triple(%s,%s,%s) = %q, want error", tt.os, tt.arch, tt.libc, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("triple(%s,%s,%s) = %q,%v; want %q", tt.os, tt.arch, tt.libc, got, err, tt.want)
		}
	}
}

func TestPickAsset(t *testing.T) {
	// Real-shaped SHA256SUMS excerpt: two patches of 3.12, a stripped variant
	// (must be ignored), a different triple, and a different minor.
	sums := `aaaa  cpython-3.12.11+20260623-aarch64-unknown-linux-musl-install_only.tar.gz
bbbb  cpython-3.12.13+20260623-aarch64-unknown-linux-musl-install_only.tar.gz
cccc  cpython-3.12.13+20260623-aarch64-unknown-linux-musl-install_only_stripped.tar.gz
dddd  cpython-3.12.13+20260623-x86_64-unknown-linux-gnu-install_only.tar.gz
eeee  cpython-3.11.15+20260623-aarch64-unknown-linux-musl-install_only.tar.gz
`
	name, sha, full, err := pickAsset(sums, "3.12", "20260623", "aarch64-unknown-linux-musl")
	if err != nil {
		t.Fatalf("pickAsset: %v", err)
	}
	if full != "3.12.13" {
		t.Errorf("full = %q, want 3.12.13 (highest patch)", full)
	}
	if sha != "bbbb" {
		t.Errorf("sha = %q, want bbbb (non-stripped)", sha)
	}
	if name != "cpython-3.12.13+20260623-aarch64-unknown-linux-musl-install_only.tar.gz" {
		t.Errorf("name = %q", name)
	}

	if _, _, _, err := pickAsset(sums, "3.13", "20260623", "aarch64-unknown-linux-musl"); err == nil {
		t.Error("expected error for an unavailable minor version")
	}
}

// TestPushTarGz pushes a synthetic PBS-shaped tar.gz through a real ExecFS over
// the host shell, then checks the executable bit and symlink landed correctly.
func TestPushTarGz(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	writeHdr := func(h *tar.Header, body []byte) {
		h.Size = int64(len(body))
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if len(body) > 0 {
			if _, err := tw.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeHdr(&tar.Header{Name: "python/bin/", Typeflag: tar.TypeDir, Mode: 0o755}, nil)
	writeHdr(&tar.Header{Name: "python/bin/python3.12", Typeflag: tar.TypeReg, Mode: 0o755}, []byte("#!/bin/sh\necho hi\n"))
	writeHdr(&tar.Header{Name: "python/bin/python3", Typeflag: tar.TypeSymlink, Linkname: "python3.12", Mode: 0o777}, nil)
	writeHdr(&tar.Header{Name: "python/lib/note.txt", Typeflag: tar.TypeReg, Mode: 0o644}, []byte("data"))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	inst := NewInstaller(Config{FS: remotefs.NewExecFS(remotefstest.LocalRunner{})})
	if err := inst.pushTarGz(context.Background(), &buf, target); err != nil {
		t.Fatalf("pushTarGz: %v", err)
	}

	// Regular executable: content + exec bit.
	exe := filepath.Join(target, "bin", "python3.12")
	if b, err := os.ReadFile(exe); err != nil || string(b) != "#!/bin/sh\necho hi\n" {
		t.Errorf("python3.12 content = %q, %v", b, err)
	}
	if fi, err := os.Stat(exe); err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("python3.12 mode = %v, want executable", fi.Mode())
	}
	// Symlink: points at python3.12 and resolves to its content.
	link := filepath.Join(target, "bin", "python3")
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("python3 is not a symlink: %v, %v", fi, err)
	}
	if tgt, err := os.Readlink(link); err != nil || tgt != "python3.12" {
		t.Errorf("python3 -> %q, %v; want python3.12", tgt, err)
	}
	// Non-exec file stays non-exec.
	if fi, err := os.Stat(filepath.Join(target, "lib", "note.txt")); err != nil || fi.Mode().Perm()&0o111 != 0 {
		t.Errorf("note.txt mode = %v, want non-executable", fi.Mode())
	}
}
