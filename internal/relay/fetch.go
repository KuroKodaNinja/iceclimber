// Package relay is the shared download→verify→cache core every relay-based installer
// uses (the language runtimes, the shell, conda's channel packages): resolve() stays
// per-installer (each upstream has its own manifest shape), but fetching a resolved,
// checksum-pinned asset — with a cross-process-safe on-disk cache — is defined once.
//
// The cache is keyed by asset name under one CacheDir (shared across sandboxes by
// design: a fleet provisions N boxes from one download). Concurrent fetches of the
// same asset — goroutines or PROCESSES (two `iceclimber install` runs, a console and
// a one-shot) — are serialized by a per-asset flock, closing the shared-cache
// `.tmp` race the per-installer copies had. Content is verified by SHA256 on both
// cache hits and fresh downloads, so a truncated or tampered cache entry is
// re-fetched, never trusted.
package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Asset is a resolved, checksum-pinned downloadable artifact.
type Asset struct {
	Name   string // unique filename, the cache key (e.g. "node-v24.1.0-linux-arm64-musl.tar.gz")
	URL    string
	SHA256 string // hex. Empty = unpinned: the asset is downloaded fresh and NOT cached (a cache hit can't be validated).
}

// Pin is an operator-pinned exact runtime asset (config runtime_pins, plan §17): a full
// version for the install dir plus a checksum-verified URL. A runtime installer's resolve()
// returns this directly when a pin matches — skipping ALL upstream metadata calls (the
// air-gapped-mirror path). The four fields map 1:1 onto each installer's private resolved.
type Pin struct {
	FullVersion string // the exact version, for the install dir (e.g. "3.12.13")
	AssetName   string // cache key (typically the URL basename)
	URL         string
	SHA256      string
}

// Fetcher downloads assets into a shared, cross-process-safe cache.
type Fetcher struct {
	CacheDir string        // "" → os.TempDir()/iceclimber-cache (the installers' long-standing default)
	Client   *http.Client  // nil → http.DefaultClient
	Progress progress.Func // nil-safe (progress.Func is)
}

// Fetch returns the local path of the verified asset, downloading it on a cache miss.
func (f *Fetcher) Fetch(ctx context.Context, a Asset) (string, error) {
	cache := f.CacheDir
	if cache == "" {
		cache = filepath.Join(os.TempDir(), "iceclimber-cache")
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}

	// Unpinned asset: no way to validate a cache hit, so fetch fresh (rare — e.g. a
	// legacy md5-only conda record) into an unshared temp file.
	if a.SHA256 == "" {
		return f.fetchUnpinned(ctx, cache, a)
	}

	dst := filepath.Join(cache, a.Name)
	// Fast path: a complete, verified entry needs no lock — entries are immutable once
	// renamed into place, so a passing hash read is conclusive.
	if sum, err := fileSHA256(dst); err == nil && sum == a.SHA256 {
		return dst, nil
	}

	// Serialize the download per asset across goroutines AND processes (waiting for a concurrent
	// downloader then hitting its cache beats racing it). ctx-aware, so a cancelled/timed-out
	// install aborts the wait instead of blocking forever on a stuck peer.
	lock, err := acquireLock(ctx, dst+".lock")
	if err != nil {
		return "", err
	}
	defer lock.release()

	// Double-check under the lock — the peer we waited on likely just completed it.
	if sum, err := fileSHA256(dst); err == nil && sum == a.SHA256 {
		return dst, nil
	}
	if err := f.downloadTo(ctx, dst, a); err != nil {
		return "", err
	}
	return dst, nil
}

// downloadTo streams a.URL into dst (via .tmp + rename), verifying the SHA256.
// Called under the per-asset lock for pinned assets.
func (f *Fetcher) downloadTo(ctx context.Context, dst string, a Asset) error {
	body, length, err := f.get(ctx, a.URL)
	if err != nil {
		return fmt.Errorf("download %s: %w", a.Name, err)
	}
	defer body.Close()

	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	src := f.Progress.Reader(body, "downloading", length)
	if _, err := io.Copy(io.MultiWriter(out, h), src); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	out.Close()
	if got := hex.EncodeToString(h.Sum(nil)); a.SHA256 != "" && got != a.SHA256 {
		os.Remove(tmp)
		return fmt.Errorf("sha256 mismatch for %s: got %s, want %s", a.Name, got, a.SHA256)
	}
	return os.Rename(tmp, dst)
}

// fetchUnpinned downloads an unverifiable asset to a private temp file (caller-owned;
// it lives in the cache dir but under a unique name, cleaned by the OS temp policy).
func (f *Fetcher) fetchUnpinned(ctx context.Context, cache string, a Asset) (string, error) {
	body, length, err := f.get(ctx, a.URL)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", a.Name, err)
	}
	defer body.Close()
	out, err := os.CreateTemp(cache, a.Name+".unpinned-*")
	if err != nil {
		return "", err
	}
	src := f.Progress.Reader(body, "downloading", length)
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(out.Name())
		return "", err
	}
	out.Close()
	return out.Name(), nil
}

func (f *Fetcher) get(ctx context.Context, url string) (io.ReadCloser, int64, error) {
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	return HTTPGet(ctx, client, url)
}

// HTTPGet issues a GET and returns the body + content length, turning any non-200 into
// an error. Shared by Fetch and the installers' resolve() metadata fetches (manifests,
// SHASUMS files), which aren't pinned assets.
func HTTPGet(ctx context.Context, client *http.Client, url string) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, resp.ContentLength, nil
}

// Exists reports whether path's basename appears in its parent's listing — the
// installers' idempotency probe. List-based deliberately: ExecFS/BusyBox has no
// reliable `stat`, but `ls -1` is contract (see remotefs).
func Exists(ctx context.Context, rfs remotefs.FS, p string) (bool, error) {
	names, err := rfs.List(ctx, path.Dir(p))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	base := path.Base(p)
	for _, n := range names {
		if n == base {
			return true, nil
		}
	}
	return false, nil
}

// VerifyRun executes cmd over the runner and returns an error carrying stderr when it
// exits non-zero — the "sandbox can actually run what we pushed" proof every installer
// ends with.
func VerifyRun(ctx context.Context, r remote.Runner, cmd string) error {
	res, err := r.Run(ctx, cmd, nil)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// flock is a held cross-process advisory lock (blocking exclusive).
type flock struct{ f *os.File }

func acquireLock(ctx context.Context, path string) (*flock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	// Poll a NON-blocking flock so ctx cancellation aborts the wait — a blocking LOCK_EX would
	// ignore the deadline and hang on a stuck peer holding the lock.
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &flock{f: f}, nil
		}
		if err != syscall.EWOULDBLOCK {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (l *flock) release() {
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}
