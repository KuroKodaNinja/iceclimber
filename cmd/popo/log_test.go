package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/logfile"
)

// TestTeeLines_PassthroughAndAppend: the capping tee mirrors stdin to stdout byte-for-byte
// and appends every line to the file.
func TestTeeLines_PassthroughAndAppend(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "session.log")
	w := logfile.New(logPath, logfile.DefaultMaxBytes, logfile.DefaultKeep)
	defer w.Close()

	in := "line one\nline two\nno trailing newline"
	var out bytes.Buffer
	if err := teeLines(strings.NewReader(in), &out, w); err != nil {
		t.Fatalf("teeLines: %v", err)
	}
	if out.String() != in {
		t.Errorf("passthrough = %q, want %q (byte-for-byte)", out.String(), in)
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != in {
		t.Errorf("logged = %q, want %q", got, in)
	}
}

// TestTeeLines_CapsAndRotates: under a tiny max-bytes the file is rotated so the live log
// stays bounded (the whole point of `popo log` over `tee -a`), while stdout still gets
// everything.
func TestTeeLines_CapsAndRotates(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "session.log")
	// keep=2 → {session.log, session.log.1}; 64-byte cap forces several rotations.
	w := logfile.New(logPath, 64, 2)
	defer w.Close()

	var in strings.Builder
	for i := 0; i < 200; i++ {
		in.WriteString("some agent output line ")
		in.WriteByte(byte('0' + i%10))
		in.WriteByte('\n')
	}
	var out bytes.Buffer
	if err := teeLines(strings.NewReader(in.String()), &out, w); err != nil {
		t.Fatalf("teeLines: %v", err)
	}
	// stdout is complete (nothing dropped by the tee).
	if out.String() != in.String() {
		t.Errorf("passthrough dropped bytes: got %d, want %d", out.Len(), in.Len())
	}
	// The live file is bounded near the cap (rotation kept it small), not the full stream.
	fi, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() >= int64(in.Len()) {
		t.Errorf("live log = %d bytes, want it capped well below the %d-byte stream", fi.Size(), in.Len())
	}
	// A rotated generation exists — proof rotation actually fired.
	if _, err := os.Stat(logPath + ".1"); err != nil {
		t.Errorf("expected a rotated session.log.1: %v", err)
	}
}
