package python

import (
	"context"
	"path"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

func TestLocate(t *testing.T) {
	ctx := context.Background()
	rfs := remotefs.NewExecFS(remotefstest.LocalRunner{})
	root := t.TempDir()
	for _, d := range []string{
		"3.12.11-aarch64-musl",
		"3.12.13-aarch64-musl",
		"3.11.9-aarch64-musl",
		"3.12.13-x86_64-gnu",
	} {
		if err := rfs.Mkdir(ctx, path.Join(root, "runtimes", "python", d)); err != nil {
			t.Fatal(err)
		}
	}

	// Highest patch for the matching platform wins.
	got, err := Locate(ctx, rfs, root, "3.12", "aarch64", "musl")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if want := path.Join(root, "runtimes", "python", "3.12.13-aarch64-musl", "bin", "python3"); got != want {
		t.Errorf("Locate = %q, want %q", got, want)
	}

	// A different platform resolves to its own runtime.
	if got, err := Locate(ctx, rfs, root, "3.12", "x86_64", "gnu"); err != nil || !strings.Contains(got, "3.12.13-x86_64-gnu") {
		t.Errorf("Locate x86_64/gnu = %q, %v", got, err)
	}

	// Not installed → error naming the install command.
	if _, err := Locate(ctx, rfs, root, "3.13", "aarch64", "musl"); err == nil {
		t.Error("expected error for uninstalled minor 3.13")
	}

	// Empty tree → not-installed error, not a transport error.
	if _, err := Locate(ctx, rfs, t.TempDir(), "3.12", "aarch64", "musl"); err == nil {
		t.Error("expected not-installed error for an empty root")
	}
}
