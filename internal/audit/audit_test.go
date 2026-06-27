package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "audit.jsonl")
	l := New(path)
	if err := l.Append(Entry{Type: "web.fetch", URL: "https://a", Method: "GET", Venue: "sandbox-exec", StatusCode: 200, Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(Entry{Type: "web.fetch", URL: "https://b", Method: "POST", StatusCode: 404, Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var entries []Entry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("line not valid JSON: %v", err)
		}
		entries = append(entries, e)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d lines, want 2", len(entries))
	}
	if entries[0].URL != "https://a" || entries[0].TS == "" || entries[1].StatusCode != 404 {
		t.Errorf("unexpected entries: %+v", entries)
	}
}

func TestAppend_DisabledIsNoOp(t *testing.T) {
	if err := New("").Append(Entry{URL: "x"}); err != nil {
		t.Errorf("disabled logger should no-op, got %v", err)
	}
}
