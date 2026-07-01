package protocol

import (
	"context"
	"os"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

// TestIsBootstrapped: a fresh root is not bootstrapped; writing skill/NANA.md (what provision
// does) flips it to true. EnsureTree alone (dirs, no NANA.md) is NOT enough — the marker is
// the file, so a bare tree still reads as unprovisioned.
func TestIsBootstrapped(t *testing.T) {
	ctx := context.Background()
	fs := remotefs.NewExecFS(remotefstest.LocalRunner{})
	tree := Tree{Root: t.TempDir()}

	if ok, err := IsBootstrapped(ctx, fs, tree); err != nil || ok {
		t.Fatalf("fresh root: IsBootstrapped = %v, %v; want false, nil", ok, err)
	}

	// A bare tree (dirs only) is still not bootstrapped — provision also writes NANA.md.
	if err := EnsureTree(ctx, fs, tree); err != nil {
		t.Fatal(err)
	}
	if ok, _ := IsBootstrapped(ctx, fs, tree); ok {
		t.Error("EnsureTree alone should NOT count as bootstrapped (no NANA.md)")
	}

	// Write the marker → bootstrapped.
	if err := os.WriteFile(tree.SkillFile(), []byte("# NANA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, err := IsBootstrapped(ctx, fs, tree); err != nil || !ok {
		t.Errorf("after writing NANA.md: IsBootstrapped = %v, %v; want true, nil", ok, err)
	}
}
