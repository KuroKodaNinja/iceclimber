package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
)

func TestRenderActivity(t *testing.T) {
	tests := []struct {
		name string
		ev   activity.Event
		want string
	}{
		{"serviced", activity.Event{TS: "2026-06-28T12:00:00Z", Kind: activity.KindServiced, Type: "python.install", Status: "ok", Detail: "python 3.12.13"}, "python.install"},
		{"approved", activity.Event{TS: "2026-06-28T12:00:00Z", Kind: activity.KindApproved, Detail: "https://xkcd.com/info.0.json"}, "approved https://xkcd.com/info.0.json"},
		{"denied", activity.Event{TS: "2026-06-28T12:00:00Z", Kind: activity.KindDenied, Detail: "https://evil/"}, "denied https://evil/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := json.Marshal(tt.ev)
			got := renderActivity(string(b))
			if !strings.HasPrefix(got, "[POPO] ") || !strings.Contains(got, tt.want) {
				t.Errorf("renderActivity = %q, want [POPO]-prefixed containing %q", got, tt.want)
			}
		})
	}
	if got := renderActivity("not json"); got != "" {
		t.Errorf("unparseable line should render empty, got %q", got)
	}
}

func TestPollFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.log")

	// Missing file: nothing, offset unchanged.
	if lines, off := pollFile(path, 0); len(lines) != 0 || off != 0 {
		t.Fatalf("missing file: lines=%v off=%d", lines, off)
	}

	// Two complete lines + a partial trailing line (no newline yet).
	if err := os.WriteFile(path, []byte("one\ntwo\npar"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, off := pollFile(path, 0)
	if len(lines) != 2 || lines[0] != "one" || lines[1] != "two" {
		t.Fatalf("first poll lines=%v (partial 'par' should be withheld)", lines)
	}

	// Completing the partial + a new line is now consumed.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("tial\nthree\n")
	f.Close()
	lines, off2 := pollFile(path, off)
	if len(lines) != 2 || lines[0] != "partial" || lines[1] != "three" {
		t.Fatalf("second poll lines=%v", lines)
	}

	// Truncation/rotation resets the offset.
	if err := os.WriteFile(path, []byte("fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if lines, _ := pollFile(path, off2); len(lines) != 1 || lines[0] != "fresh" {
		t.Fatalf("after truncation lines=%v", lines)
	}
}

func TestReadLinesAndLastN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jsonl")
	os.WriteFile(path, []byte("a\nb\nc\n"), 0o644)
	lines, off := readLines(path)
	if len(lines) != 3 || off != 6 {
		t.Fatalf("readLines = %v off=%d", lines, off)
	}
	if got := lastN(lines, 2); len(got) != 2 || got[0] != "b" {
		t.Fatalf("lastN(2) = %v", got)
	}
	if got := lastN(lines, 0); len(got) != 3 {
		t.Fatalf("lastN(0) should be all, got %v", got)
	}
}
