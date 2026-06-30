package node

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

// makeTarGz writes a minimal gzip'd tar with a single top-level dir (which the
// push strips) containing one file, and returns its path.
func makeTarGz(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "runtime.tar.gz")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	body := []byte("#!/bin/sh\necho ok\n")
	for _, h := range []*tar.Header{
		{Name: "python/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "python/bin/python3", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	}
	tw.Close()
	gw.Close()
	return p
}

// TestExtractAndPush_ReportsTransferProgress proves the push reports byte progress
// against the compressed tarball size and flushes a final full-count event — over a
// real (local) ExecFS extraction.
func TestExtractAndPush_ReportsTransferProgress(t *testing.T) {
	dir := t.TempDir()
	tarball := makeTarGz(t, dir)
	size, _ := os.Stat(tarball)

	var events []progress.Event
	inst := &Installer{cfg: Config{
		FS:       remotefs.NewExecFS(remotefstest.LocalRunner{}),
		Progress: func(e progress.Event) { events = append(events, e) },
	}}
	target := filepath.Join(dir, "out")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := inst.extractAndPush(context.Background(), tarball, target); err != nil {
		t.Fatalf("extractAndPush: %v", err)
	}

	if len(events) == 0 {
		t.Fatal("no progress events emitted during transfer")
	}
	last := events[len(events)-1]
	if last.Phase != "transferring" || last.Unit != progress.Bytes {
		t.Errorf("event = %+v, want a transferring/Bytes event", last)
	}
	if last.Total != size.Size() || last.Cur != size.Size() {
		t.Errorf("final event = %d/%d, want full compressed size %d", last.Cur, last.Total, size.Size())
	}
	// The top-level component was stripped and the file extracted.
	if _, err := os.Stat(filepath.Join(target, "bin", "python3")); err != nil {
		t.Errorf("expected extracted file: %v", err)
	}
}
