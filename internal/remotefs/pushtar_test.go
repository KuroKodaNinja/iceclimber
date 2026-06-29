package remotefs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"testing"
)

func TestStripLead(t *testing.T) {
	cases := map[string]string{
		"python/bin/python3":    "bin/python3",
		"jdk-21.0.11+10/README": "README",
		"./top/x":               "x",
		"top":                   "",
		"top/":                  "",
		"":                      "",
	}
	for in, want := range cases {
		if got := stripLead(in); got != want {
			t.Errorf("stripLead(%q) = %q, want %q", in, got, want)
		}
	}
}

// gzTar builds a gzipped tar with the given entries (a top-level dir that the push
// must strip, an executable file, a symlink, and a nested file).
func gzTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	write := func(h *tar.Header, body string) {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(&tar.Header{Name: "top/", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	write(&tar.Header{Name: "top/bin/", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	write(&tar.Header{Name: "top/bin/run", Typeflag: tar.TypeReg, Mode: 0o755, Size: 3}, "hi\n")
	write(&tar.Header{Name: "top/bin/link", Typeflag: tar.TypeSymlink, Linkname: "run"}, "")
	write(&tar.Header{Name: "top/lib/note.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2}, "x\n")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// recFS records what a per-file push writes.
type recFS struct {
	files map[string][]byte
	modes map[string]os.FileMode
	links map[string]string
}

func newRecFS() *recFS {
	return &recFS{files: map[string][]byte{}, modes: map[string]os.FileMode{}, links: map[string]string{}}
}

func (f *recFS) Mkdir(context.Context, string) error                    { return nil }
func (f *recFS) WriteFile(_ context.Context, p string, d []byte) error  { f.files[p] = d; return nil }
func (f *recFS) ReadFile(_ context.Context, p string) ([]byte, error)   { return f.files[p], nil }
func (f *recFS) List(context.Context, string) ([]string, error)         { return nil, nil }
func (f *recFS) Rename(context.Context, string, string) error           { return nil }
func (f *recFS) Chmod(_ context.Context, p string, m os.FileMode) error { f.modes[p] = m; return nil }
func (f *recFS) Symlink(_ context.Context, target, link string) error {
	f.links[link] = target
	return nil
}
func (f *recFS) RemoveAll(context.Context, string) error { return nil }

// PushTarGz over a plain FS (no TreePusher) writes each entry, stripping the top
// dir and preserving the executable bit and symlink.
func TestPushTarGz_PerFile(t *testing.T) {
	fs := newRecFS()
	if err := PushTarGz(context.Background(), fs, bytes.NewReader(gzTar(t)), "/dest"); err != nil {
		t.Fatal(err)
	}
	if string(fs.files["/dest/bin/run"]) != "hi\n" {
		t.Errorf("bin/run not written under /dest: %v", fs.files)
	}
	if fs.modes["/dest/bin/run"] != 0o755 {
		t.Errorf("bin/run mode = %o, want 755", fs.modes["/dest/bin/run"])
	}
	if fs.links["/dest/bin/link"] != "run" {
		t.Errorf("symlink not recreated: %v", fs.links)
	}
	if _, ok := fs.files["/dest/lib/note.txt"]; !ok {
		t.Errorf("nested file missing: %v", fs.files)
	}
	if _, leaked := fs.files["/dest/top/bin/run"]; leaked {
		t.Errorf("top-level component was not stripped")
	}
}

// bulkFS is a TreePusher that captures the streamed tar.
type bulkFS struct {
	*recFS
	target   string
	captured bytes.Buffer
}

func (b *bulkFS) PushTar(_ context.Context, r io.Reader, target string) error {
	b.target = target
	_, err := io.Copy(&b.captured, r)
	return err
}

// PushTarGz over a TreePusher streams a single prefix-stripped tar (the bulk path).
func TestPushTarGz_Bulk(t *testing.T) {
	fs := &bulkFS{recFS: newRecFS()}
	if err := PushTarGz(context.Background(), fs, bytes.NewReader(gzTar(t)), "/dest"); err != nil {
		t.Fatal(err)
	}
	if fs.target != "/dest" {
		t.Errorf("PushTar target = %q, want /dest", fs.target)
	}
	if len(fs.files) != 0 {
		t.Errorf("bulk path must not fall back to per-file writes: %v", fs.files)
	}

	// The captured stream is a plain tar with names stripped and modes/symlink kept.
	tr := tar.NewReader(&fs.captured)
	got := map[string]*tar.Header{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got[h.Name] = h
	}
	if h := got["bin/run"]; h == nil || h.Mode != 0o755 {
		t.Errorf("bin/run missing or wrong mode in streamed tar: %+v", h)
	}
	if h := got["bin/link"]; h == nil || h.Typeflag != tar.TypeSymlink || h.Linkname != "run" {
		t.Errorf("symlink missing in streamed tar: %+v", h)
	}
	if got["top"] != nil || got["top/bin/run"] != nil {
		t.Errorf("top-level component was not stripped in the streamed tar")
	}
}
