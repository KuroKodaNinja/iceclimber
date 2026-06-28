package activity

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoggerAppendRoundTrip(t *testing.T) {
	// Parent dir intentionally absent — Append must create it.
	path := filepath.Join(t.TempDir(), "sub", "activity.jsonl")
	l := New(path)

	want := []Event{
		{Kind: KindServiced, ID: "a1", Type: "ping", Status: "ok", DurMS: 3},
		{Kind: KindServiced, ID: "a2", Type: "python.install", Status: "ok", Detail: "3.12.13"},
		{Kind: KindApproved, Detail: "https://xkcd.com/info.0.json"},
	}
	for _, e := range want {
		if err := l.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	var got []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("unmarshal %q: %v", sc.Text(), err)
		}
		got = append(got, e)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	if got[0].TS == "" {
		t.Error("ts was not stamped")
	}
	if got[1].Type != "python.install" || got[1].Detail != "3.12.13" {
		t.Errorf("event 2 mismatch: %+v", got[1])
	}
	if got[2].Kind != KindApproved {
		t.Errorf("event 3 kind = %q, want %q", got[2].Kind, KindApproved)
	}
}

func TestLoggerEmptyPathIsNoop(t *testing.T) {
	l := New("")
	if err := l.Append(Event{Kind: KindServiced}); err != nil {
		t.Fatalf("empty-path append should be a no-op, got %v", err)
	}
	if l.Path() != "" {
		t.Errorf("path = %q, want empty", l.Path())
	}
}
