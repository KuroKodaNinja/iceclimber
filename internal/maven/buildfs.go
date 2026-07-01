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
	"strings"
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

// expectedSHA512 fetches the Apache-published `<url>.sha512` sidecar and returns the
// hex digest (the file is the digest, optionally followed by whitespace/filename).
func expectedSHA512(ctx context.Context, url string) (string, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+".sha512", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s.sha512: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	if f := strings.Fields(string(body)); len(f) > 0 {
		return strings.ToLower(f[0]), nil
	}
	return "", fmt.Errorf("empty sha512 sidecar for %s", url)
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
