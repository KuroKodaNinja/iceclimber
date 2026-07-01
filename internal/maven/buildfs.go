package maven

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// pullTree copies a sandbox directory tree to a local directory, skipping build output
// (`target`) and VCS metadata. Directory vs file is distinguished by whether List
// succeeds (a file's List errors), since remotefs.FS has no Stat.
func pullTree(ctx context.Context, rfs remotefs.FS, remoteRoot, localRoot string) error {
	if err := os.MkdirAll(localRoot, 0o755); err != nil {
		return err
	}
	entries, err := rfs.List(ctx, remoteRoot)
	if err != nil {
		return fmt.Errorf("list %s: %w", remoteRoot, err)
	}
	for _, name := range entries {
		if name == "target" || name == ".git" || name == "node_modules" {
			continue
		}
		rpath := path.Join(remoteRoot, name)
		lpath := filepath.Join(localRoot, name)
		if _, lerr := rfs.List(ctx, rpath); lerr == nil {
			if err := pullTree(ctx, rfs, rpath, lpath); err != nil {
				return err
			}
			continue
		}
		data, rerr := rfs.ReadFile(ctx, rpath)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", rpath, rerr)
		}
		if err := os.WriteFile(lpath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// pushTree mirrors a local directory tree into the sandbox over the FS (dirs + regular
// files; a Maven repo has no symlinks). It emits coarse transfer progress.
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
			pr.Emit(progress.Event{Phase: "relaying repo", Cur: int64(i + 1), Total: int64(len(files)), Unit: progress.Items})
		}
	}
	return nil
}

// downloadTo GETs url and streams the body to w.
func downloadTo(ctx context.Context, url string, w io.Writer) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}
