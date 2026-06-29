package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

// TestResetAgentLog: a serving session must start the bridged agent log fresh, so a
// new session never shows a previous run's (or a test's) leftover [NANA] stream.
func TestResetAgentLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "agent.log") // nested dir is created
	resetAgentLog(p)
	if b, err := os.ReadFile(p); err != nil || len(b) != 0 {
		t.Fatalf("reset should create an empty file: %v, %q", err, b)
	}
	if err := os.WriteFile(p, []byte("STALE from a previous session\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resetAgentLog(p)
	if b, _ := os.ReadFile(p); len(b) != 0 {
		t.Errorf("reset did not truncate stale content: %q", b)
	}
}

// TestPollAgentLogs exercises the console's auto-discovery of agent session logs over
// a real ExecFS (local runner): a single agent yields unprefixed lines, a second poll
// returns only what was appended, a truncation restarts from the top, and a second
// installed agent makes lines name-prefixed. This is the no-`--agent-log` path that
// feeds the [NANA] pane.
func TestPollAgentLogs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	base := filepath.Join(root, "agent")
	claudeLog := filepath.Join(base, "claude", "session.log")
	if err := os.MkdirAll(filepath.Dir(claudeLog), 0o755); err != nil {
		t.Fatal(err)
	}
	fs := remotefs.NewExecFS(remotefstest.LocalRunner{})
	offsets := map[string]int{}

	// 1. Initial content → both lines, no prefix (single agent).
	if err := os.WriteFile(claudeLog, []byte("hello popo\nasking for python\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := pollAgentLogs(ctx, fs, base, offsets)
	if len(got) != 2 || got[0] != "hello popo" || got[1] != "asking for python" {
		t.Fatalf("first poll = %+v, want the two lines unprefixed", got)
	}

	// 2. Nothing new → no lines.
	if got := pollAgentLogs(ctx, fs, base, offsets); len(got) != 0 {
		t.Errorf("no-change poll = %+v, want empty", got)
	}

	// 3. Append → only the new line.
	f, err := os.OpenFile(claudeLog, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("got python 3.12\n")
	f.Close()
	if got := pollAgentLogs(ctx, fs, base, offsets); len(got) != 1 || got[0] != "got python 3.12" {
		t.Errorf("append poll = %+v, want just the new line", got)
	}

	// 4. Truncate (rotation) → restart from the top.
	if err := os.WriteFile(claudeLog, []byte("fresh start\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pollAgentLogs(ctx, fs, base, offsets); len(got) != 1 || got[0] != "fresh start" {
		t.Errorf("post-truncate poll = %+v, want the restarted line", got)
	}

	// 5. A second installed agent → lines become name-prefixed.
	otherLog := filepath.Join(base, "other", "session.log")
	if err := os.MkdirAll(filepath.Dir(otherLog), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherLog, []byte("other agent up\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = pollAgentLogs(ctx, fs, base, offsets)
	if len(got) != 1 || got[0] != "[other] other agent up" {
		t.Fatalf("multi-agent poll = %+v, want the new agent's line prefixed", got)
	}
}
