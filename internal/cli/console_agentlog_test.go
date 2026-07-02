package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

// TestPollAgentLogs exercises the console's auto-discovery of agent session logs over a real
// ExecFS: on first sight of a log the offset seeds to EOF (no re-dump of a prior run's output),
// then only newly-appended lines stream; a truncation/rotation restarts from the top; and a
// second installed agent makes lines name-prefixed. This is the no-`--agent-log` [NANA] path.
func TestPollAgentLogs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	base := filepath.Join(root, "agent")
	claudeLog := filepath.Join(base, "claude", "session.log")
	if err := os.MkdirAll(filepath.Dir(claudeLog), 0o755); err != nil {
		t.Fatal(err)
	}
	fs := remotefs.NewExecFS(remotefstest.LocalRunner{})
	offsets := map[string]int64{}

	appendTo := func(p, s string) {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}

	// 1. Pre-existing content → first poll SEEDS to EOF and emits nothing (no re-dump).
	if err := os.WriteFile(claudeLog, []byte("old 1\nold 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pollAgentLogs(ctx, fs, base, offsets); len(got) != 0 {
		t.Fatalf("first poll should seed to EOF (no re-dump), got %+v", got)
	}

	// 2. New output after seeding → streamed, unprefixed (single agent).
	appendTo(claudeLog, "hello popo\nasking for python\n")
	got := pollAgentLogs(ctx, fs, base, offsets)
	if len(got) != 2 || got[0] != "hello popo" || got[1] != "asking for python" {
		t.Fatalf("after-seed poll = %+v, want the two new lines unprefixed", got)
	}

	// 3. Nothing new → no lines. 4. Append → only the new line.
	if got := pollAgentLogs(ctx, fs, base, offsets); len(got) != 0 {
		t.Errorf("no-change poll = %+v, want empty", got)
	}
	appendTo(claudeLog, "got python 3.12\n")
	if got := pollAgentLogs(ctx, fs, base, offsets); len(got) != 1 || got[0] != "got python 3.12" {
		t.Errorf("append poll = %+v, want just the new line", got)
	}

	// 5. Truncate (rotation) → ReadFrom restarts from the top on a SEEN file → emits the new content.
	if err := os.WriteFile(claudeLog, []byte("fresh start\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pollAgentLogs(ctx, fs, base, offsets); len(got) != 1 || got[0] != "fresh start" {
		t.Errorf("post-truncate poll = %+v, want the restarted line", got)
	}

	// 6. A second installed agent → its first sight seeds to EOF (nothing); then new output from
	//    both is name-prefixed (multi-agent), sorted by name (claude before other).
	otherLog := filepath.Join(base, "other", "session.log")
	if err := os.MkdirAll(filepath.Dir(otherLog), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherLog, []byte("other seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pollAgentLogs(ctx, fs, base, offsets); len(got) != 0 {
		t.Fatalf("new agent's first sight should seed to EOF, got %+v", got)
	}
	appendTo(claudeLog, "claude too\n")
	appendTo(otherLog, "other up\n")
	got = pollAgentLogs(ctx, fs, base, offsets)
	if len(got) != 2 || got[0] != "[claude] claude too" || got[1] != "[other] other up" {
		t.Fatalf("multi-agent poll = %+v, want both new lines name-prefixed (sorted)", got)
	}
}
