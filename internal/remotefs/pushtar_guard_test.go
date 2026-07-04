package remotefs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"strings"
	"testing"
)

// gzTarOne builds a gzipped tar with a single top-level dir plus one entry of the given name —
// used to feed PushTarGz a path that escapes the target after the top-level strip.
func gzTarOne(t *testing.T, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "top/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestPushTarGzRejectsZipSlip: an archive entry whose stripped path escapes the target (a ".."
// traversal) must fail extraction rather than write outside the sandbox install root — the
// compromised-mirror guard (the tarball AND its SHASUMS share an origin).
func TestPushTarGzRejectsZipSlip(t *testing.T) {
	for _, name := range []string{"top/../../etc/passwd", "top/../sibling/x"} {
		fs := newRecFS()
		err := PushTarGz(context.Background(), fs, bytes.NewReader(gzTarOne(t, name)), "/root/target")
		if err == nil || !strings.Contains(err.Error(), "unsafe path") {
			t.Errorf("entry %q: err = %v, want an 'unsafe path' rejection", name, err)
		}
		if len(fs.files) != 0 {
			t.Errorf("entry %q: wrote %d files despite the escape, want 0", name, len(fs.files))
		}
	}

	// A normal nested entry still extracts fine (the guard doesn't over-reject).
	fs := newRecFS()
	if err := PushTarGz(context.Background(), fs, bytes.NewReader(gzTarOne(t, "top/bin/tool")), "/root/target"); err != nil {
		t.Fatalf("legitimate nested entry should extract: %v", err)
	}
	if _, ok := fs.files["/root/target/bin/tool"]; !ok {
		t.Errorf("expected /root/target/bin/tool written, got %v", fs.files)
	}
}
