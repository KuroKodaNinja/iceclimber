//go:build functional

package functional

import (
	"context"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestSFTPRemoveAllWipesRuntimeTree guards `bootstrap --force`'s destructive reset over SFTP:
// its recursive RemoveAll must fully wipe a real installed runtime tree (thousands of files +
// relocatable-interpreter symlinks), not abort partway. This was written to chase a suspected
// wipe bug (a flaky test leaked a runtime root); it confirmed SFTP RemoveAll is in fact correct,
// and now stands as the regression guard so it stays that way. Elsewhere the reset tests mask
// RemoveAll behavior with limactl-based cleanup — this one exercises the real code path.
func TestSFTPRemoveAllWipesRuntimeTree(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-rmall-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root) // schedules limactl cleanup as a backstop

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")
	// A real runtime tree (thousands of files + symlinks) — the shape that tripped RemoveAll.
	runIceclimber(t, "install", "python", "3.12", "--config", cfg, "--transport", "sftp")

	fs, cleanup := dialFS(t, sb, "sftp")
	defer cleanup()
	ctx := context.Background()

	if err := fs.RemoveAll(ctx, root); err != nil {
		t.Fatalf("SFTP RemoveAll(%s) returned an error (bootstrap --force would leak): %v", root, err)
	}
	// The root must be gone — List should fail (not-exist) or return empty.
	if entries, err := fs.List(ctx, root); err == nil && len(entries) > 0 {
		t.Errorf("SFTP RemoveAll left %d entries under %s: %v", len(entries), root, entries)
	}
}
