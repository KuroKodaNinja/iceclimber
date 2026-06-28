package tail

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReader_HistoryAndPoll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.log")

	// Missing file: nothing.
	r := NewReader(path)
	if got := r.History(); got != nil {
		t.Fatalf("missing-file History = %v", got)
	}
	if got := r.Poll(); got != nil {
		t.Fatalf("missing-file Poll = %v", got)
	}

	// Three complete lines → History returns them and advances to EOF.
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = NewReader(path)
	if hist := r.History(); len(hist) != 3 || hist[0] != "a" || hist[2] != "c" {
		t.Fatalf("History = %v", hist)
	}
	if got := r.Poll(); got != nil {
		t.Fatalf("Poll right after History should be empty, got %v", got)
	}

	// Append a complete line + a partial trailing line: only the complete one.
	appendStr(t, path, "d\npar")
	if got := r.Poll(); len(got) != 1 || got[0] != "d" {
		t.Fatalf("Poll = %v (partial 'par' should be withheld)", got)
	}
	// Completing the partial consumes it.
	appendStr(t, path, "tial\n")
	if got := r.Poll(); len(got) != 1 || got[0] != "partial" {
		t.Fatalf("Poll completed = %v", got)
	}

	// Truncation/rotation resets to the start.
	if err := os.WriteFile(path, []byte("fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := r.Poll(); len(got) != 1 || got[0] != "fresh" {
		t.Fatalf("Poll after truncation = %v", got)
	}
}

func TestLastN(t *testing.T) {
	lines := []string{"a", "b", "c"}
	if got := LastN(lines, 2); len(got) != 2 || got[0] != "b" {
		t.Fatalf("LastN(2) = %v", got)
	}
	if got := LastN(lines, 0); len(got) != 3 {
		t.Fatalf("LastN(0) should be all, got %v", got)
	}
	if got := LastN(lines, 9); len(got) != 3 {
		t.Fatalf("LastN(>len) should be all, got %v", got)
	}
}

func appendStr(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	f.Close()
}
