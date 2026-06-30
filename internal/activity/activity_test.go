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

func TestReadAndCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.jsonl")

	// Missing file → no events, no error (so counters can seed from a not-yet-written log).
	if evs, err := Read(path); err != nil || len(evs) != 0 {
		t.Fatalf("Read(missing) = %v, %v; want empty/nil", evs, err)
	}

	l := New(path)
	for _, e := range []Event{
		{Kind: KindServiced, ID: "1"}, {Kind: KindServiced, ID: "2"},
		{Kind: KindApproved}, {Kind: KindDenied},
		{Kind: KindOperated}, {Kind: KindVerified, Side: SideNana},
	} {
		if err := l.Append(e); err != nil {
			t.Fatal(err)
		}
	}

	evs, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != 6 {
		t.Fatalf("Read returned %d events, want 6", len(evs))
	}
	s, a, d := Counts(evs)
	if s != 2 || a != 1 || d != 1 {
		t.Errorf("Counts = served %d, approved %d, denied %d; want 2/1/1 (operated+verified excluded)", s, a, d)
	}
}

func TestCounts_IgnoresStarted(t *testing.T) {
	// KindStarted is live-only and never written, but a stray line must not inflate the
	// serviced tally — in-progress is not completion.
	evs := []Event{
		{Kind: KindStarted, ID: "1", Type: "ping"},
		{Kind: KindServiced, ID: "1", Type: "ping", Status: "ok"},
		{Kind: KindStarted, ID: "2", Type: "pip.install"},
	}
	if s, a, d := Counts(evs); s != 1 || a != 0 || d != 0 {
		t.Errorf("Counts = served %d, approved %d, denied %d; want 1/0/0 (started ignored)", s, a, d)
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
