package cargo

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// pushTree mirrors a local directory tree (the controller's cargo vendor dir) into the sandbox
// over the FS (dirs + regular files; a vendor tree has no symlinks). It emits coarse transfer
// progress. Mirrors maven's pushTree.
func pushTree(ctx context.Context, rfs remotefs.FS, pr progress.Func, localRoot, remoteRoot string) error {
	if err := rfs.Mkdir(ctx, remoteRoot); err != nil {
		return err
	}
	var files []string
	if err := filepath.WalkDir(localRoot, func(p string, dEntry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !dEntry.IsDir() {
			files = append(files, p)
		}
		return nil
	}); err != nil {
		return err
	}
	for i, p := range files {
		rel, err := filepath.Rel(localRoot, p)
		if err != nil {
			return err
		}
		dst := path.Join(remoteRoot, filepath.ToSlash(rel))
		if err := rfs.Mkdir(ctx, path.Dir(dst)); err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := rfs.WriteFile(ctx, dst, data); err != nil {
			return fmt.Errorf("relay %s: %w", rel, err)
		}
		if (i+1)%25 == 0 || i+1 == len(files) {
			pr.Emit(progress.Event{Phase: "relaying vendor", Cur: int64(i + 1), Total: int64(len(files)), Unit: progress.Items})
		}
	}
	return nil
}
