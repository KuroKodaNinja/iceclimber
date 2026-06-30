package cli

import (
	"context"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

// TestCollectStatus exercises the status snapshot over a real ExecFS/LocalRunner at a
// temp root — the first unit coverage of queue counts + runtime health-probe + caps,
// the area that shipped several observability bugs untested.
func TestCollectStatus(t *testing.T) {
	ctx := context.Background()
	runner := remotefstest.LocalRunner{}
	fs := remotefs.NewExecFS(runner)
	tree := protocol.Tree{Root: t.TempDir()}
	if err := protocol.EnsureTree(ctx, fs, tree); err != nil {
		t.Fatalf("EnsureTree: %v", err)
	}

	// Heartbeat seq 5, fresh.
	if err := fs.WriteFile(ctx, tree.Heartbeat(), []byte("5 "+time.Now().UTC().Format(time.RFC3339)+"\n")); err != nil {
		t.Fatal(err)
	}
	// Queue: 2 awaiting (outbox/new), 3 delivered-on-disk (inbox/new).
	for i, dir := range []string{tree.Outbox().New(), tree.Outbox().New(), tree.Inbox().New(), tree.Inbox().New(), tree.Inbox().New()} {
		if err := fs.WriteFile(ctx, path.Join(dir, string(rune('a'+i))+".json"), []byte("{}")); err != nil {
			t.Fatal(err)
		}
	}
	// A healthy python (executable, exits 0) and a broken node (not executable → fails).
	pyBin := path.Join(tree.Root, "runtimes", "python", "3.12.13-x", "bin")
	if err := fs.Mkdir(ctx, pyBin); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(ctx, path.Join(pyBin, "python3"), []byte("#!/bin/sh\necho 'Python 3.12.13'\n")); err != nil {
		t.Fatal(err)
	}
	if err := fs.Chmod(ctx, path.Join(pyBin, "python3"), 0o755); err != nil {
		t.Fatal(err)
	}
	nodeBin := path.Join(tree.Root, "runtimes", "node", "24.0.0-x", "bin")
	if err := fs.Mkdir(ctx, nodeBin); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(ctx, path.Join(nodeBin, "node"), []byte("not a binary")); err != nil { // 0644, won't exec
		t.Fatal(err)
	}

	s := collectStatus(ctx, fs, runner, tree)

	if s.HeartbeatSeq != "5" {
		t.Errorf("HeartbeatSeq = %q, want 5", s.HeartbeatSeq)
	}
	if s.QueueOut != 2 || s.QueueIn != 3 {
		t.Errorf("queue = %d awaiting / %d delivered, want 2/3", s.QueueOut, s.QueueIn)
	}
	runtimes := strings.Join(s.Runtimes, " | ")
	if !strings.Contains(runtimes, "python 3.12.13-x ✓") {
		t.Errorf("healthy python should be ✓; got %q", runtimes)
	}
	if !strings.Contains(runtimes, "node 24.0.0-x ✗") {
		t.Errorf("broken node should be ✗ (won't run); got %q", runtimes)
	}
	if s.Caps != "" {
		t.Errorf("Caps = %q, want empty (no capabilities.json written)", s.Caps)
	}
}
