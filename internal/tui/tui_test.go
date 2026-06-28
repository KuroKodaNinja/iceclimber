package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNew_HistoryAndCounts(t *testing.T) {
	dir := t.TempDir()
	ap := filepath.Join(dir, "activity.jsonl")
	os.WriteFile(ap, []byte(`{"ts":"2026-06-28T18:00:01Z","kind":"serviced","type":"ping","status":"ok"}
{"ts":"2026-06-28T18:00:02Z","kind":"approved","detail":"https://x/"}
{"ts":"2026-06-28T18:00:03Z","kind":"denied","detail":"https://y/"}
`), 0o644)
	m := New("sbx", ap, "")
	if m.served != 1 || m.approved != 1 || m.denied != 1 {
		t.Fatalf("counts = %d/%d/%d, want 1/1/1", m.served, m.approved, m.denied)
	}
	if len(m.popoLines) != 3 {
		t.Fatalf("popoLines = %d, want 3", len(m.popoLines))
	}
}

func TestUpdate_TickPollsNewLines(t *testing.T) {
	dir := t.TempDir()
	ap := filepath.Join(dir, "activity.jsonl")
	os.WriteFile(ap, []byte(`{"ts":"2026-06-28T18:00:01Z","kind":"serviced","type":"ping","status":"ok"}`+"\n"), 0o644)
	agent := filepath.Join(dir, "agent.log")
	os.WriteFile(agent, []byte("hello\n"), 0o644)

	m := New("sbx", ap, agent)
	if m.served != 1 || len(m.nanaLines) != 1 {
		t.Fatalf("seed: served=%d nana=%d", m.served, len(m.nanaLines))
	}

	appendFile(t, ap, `{"ts":"2026-06-28T18:00:02Z","kind":"serviced","type":"python.install","status":"ok"}`+"\n")
	appendFile(t, agent, "world\n")

	updated, cmd := m.Update(tickMsg(time.Now()))
	m2 := updated.(Model)
	if m2.served != 2 || len(m2.nanaLines) != 2 {
		t.Fatalf("after tick: served=%d nana=%d, want 2/2", m2.served, len(m2.nanaLines))
	}
	if cmd == nil {
		t.Error("tick should reschedule itself (non-nil cmd)")
	}
}

func TestUpdate_QuitKey(t *testing.T) {
	m := New("sbx", filepath.Join(t.TempDir(), "x.jsonl"), "")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q should return a command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q should return the Quit command")
	}
}

func TestView_ContainsHeaderAndPanes(t *testing.T) {
	dir := t.TempDir()
	ap := filepath.Join(dir, "activity.jsonl")
	os.WriteFile(ap, []byte(`{"ts":"2026-06-28T18:00:01Z","kind":"serviced","type":"python.install","status":"ok","detail":"python 3.12.13"}`+"\n"), 0o644)
	m := New("my-sbx", ap, filepath.Join(dir, "agent.log"))
	v := m.View()
	for _, want := range []string{"my-sbx", "[POPO]", "[NANA]", "python.install", "serviced 1"} {
		if !strings.Contains(v, want) {
			t.Errorf("View missing %q:\n%s", want, v)
		}
	}
}

func TestStripANSI(t *testing.T) {
	if got := stripANSI("\x1b[31mred\x1b[0m text"); got != "red text" {
		t.Errorf("stripANSI = %q, want %q", got, "red text")
	}
}

func appendFile(t *testing.T, path, s string) {
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
