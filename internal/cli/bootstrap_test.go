package cli

import (
	"context"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/probe"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

// TestGuardResettableRoot: `bootstrap --force` may only wipe a dedicated sandbox dir — a
// shallow or empty root must be refused so a misconfig can't rm -rf something important.
func TestGuardResettableRoot(t *testing.T) {
	ok := []string{"/tmp/iceclimber-x", "/home/bwitt.guest/iceclimber-demo", "/opt/ice/sbx"}
	for _, r := range ok {
		if err := guardResettableRoot(r); err != nil {
			t.Errorf("guardResettableRoot(%q) = %v, want nil (a dedicated sandbox dir)", r, err)
		}
	}
	bad := []string{"", "/", ".", "/tmp", "/home", "///"}
	for _, r := range bad {
		if err := guardResettableRoot(r); err == nil {
			t.Errorf("guardResettableRoot(%q) = nil, want refusal (unsafe/shallow root)", r)
		}
	}
}

// TestSmokeTest_LeavesMaildirClean: a bootstrap's smoke test collects its own pong and
// GC-prunes the pair, so it leaves no permanent "1 uncollected" behind.
func TestSmokeTest_LeavesMaildirClean(t *testing.T) {
	ctx := context.Background()
	fs := remotefs.NewExecFS(remotefstest.LocalRunner{})
	tree := protocol.Tree{Root: t.TempDir()}
	if err := protocol.EnsureTree(ctx, fs, tree); err != nil {
		t.Fatal(err)
	}
	sess := &session{fs: fs, tree: tree, fp: &probe.Fingerprint{}}
	disp := protocol.NewDispatcher(fs, tree, buildRegistry(sess, nil))
	if err := smokeTest(ctx, fs, tree, disp); err != nil {
		t.Fatalf("smokeTest: %v", err)
	}
	for _, dir := range []string{tree.Inbox().New(), tree.Inbox().Cur(), tree.Outbox().Cur()} {
		if n, _ := fs.List(ctx, dir); len(n) != 0 {
			t.Errorf("%s not empty after smokeTest (collect+prune failed): %v", dir, n)
		}
	}
}
