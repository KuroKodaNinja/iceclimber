package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// TestFetch_CacheAndVerify: a fetch downloads + verifies; a second fetch is a pure cache
// hit (no HTTP); a corrupted cache entry is re-downloaded, never trusted; a checksum
// mismatch from upstream is a hard error that leaves no cache entry behind.
func TestFetch_CacheAndVerify(t *testing.T) {
	payload := []byte("the artifact bytes")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	f := &Fetcher{CacheDir: t.TempDir()}
	a := Asset{Name: "art.bin", URL: srv.URL, SHA256: sum(payload)}

	p1, err := f.Fetch(context.Background(), a)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got, _ := os.ReadFile(p1); string(got) != string(payload) {
		t.Error("cached bytes differ from upstream")
	}
	if _, err := f.Fetch(context.Background(), a); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("second fetch should be a cache hit; upstream hit %d times", hits.Load())
	}

	// Corrupt the cache entry → verification fails the fast path → re-download.
	if err := os.WriteFile(p1, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Fetch(context.Background(), a); err != nil {
		t.Fatalf("fetch over corrupted cache: %v", err)
	}
	if hits.Load() != 2 {
		t.Errorf("corrupted entry should force a re-download; hits=%d", hits.Load())
	}
	if got, _ := os.ReadFile(p1); string(got) != string(payload) {
		t.Error("cache not healed after corruption")
	}

	// Upstream content not matching the pin → error, and no poisoned cache entry.
	bad := Asset{Name: "bad.bin", URL: srv.URL, SHA256: sum([]byte("something else"))}
	if _, err := f.Fetch(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("want sha256 mismatch, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.CacheDir, "bad.bin")); !os.IsNotExist(err) {
		t.Error("a mismatched download must not land in the cache")
	}
}

// TestFetch_ConcurrentSameAsset: N concurrent fetchers of one asset produce ONE download
// (the flock serializes; the double-check under the lock turns the rest into cache hits).
// flock is per open-file-description, so in-process concurrency exercises the same
// contention two processes would.
func TestFetch_ConcurrentSameAsset(t *testing.T) {
	payload := []byte(strings.Repeat("x", 1<<16))
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	cache := t.TempDir()
	a := Asset{Name: "big.bin", URL: srv.URL, SHA256: sum(payload)}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// A fresh Fetcher per goroutine — like distinct processes sharing CacheDir.
			f := &Fetcher{CacheDir: cache}
			_, errs[i] = f.Fetch(context.Background(), a)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("fetcher %d: %v", i, err)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("8 concurrent fetches should download once; upstream hit %d times", hits.Load())
	}
}

// TestFetch_UnpinnedNeverCached: an asset without a SHA256 is fetched fresh every time
// (a cache hit can't be validated) into a private temp file.
func TestFetch_UnpinnedNeverCached(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("unpinned"))
	}))
	defer srv.Close()

	f := &Fetcher{CacheDir: t.TempDir()}
	a := Asset{Name: "loose.bin", URL: srv.URL}
	p1, err := f.Fetch(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := f.Fetch(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if p1 == p2 {
		t.Error("unpinned fetches must not share a path (no validatable cache)")
	}
	if hits.Load() != 2 {
		t.Errorf("unpinned fetches must hit upstream every time; hits=%d", hits.Load())
	}
}

// TestExistsAndVerifyRun covers the two sandbox-side helpers over a real local ExecFS.
func TestExistsAndVerifyRun(t *testing.T) {
	ctx := context.Background()
	rfs := remotefs.NewExecFS(remotefstest.LocalRunner{})
	dir := t.TempDir()
	p := filepath.Join(dir, "bin")
	if ok, err := Exists(ctx, rfs, p); err != nil || ok {
		t.Fatalf("missing file: ok=%v err=%v", ok, err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ok, err := Exists(ctx, rfs, p); err != nil || !ok {
		t.Fatalf("present file: ok=%v err=%v", ok, err)
	}
	// A missing PARENT dir is "doesn't exist", not an error (fresh sandbox).
	if ok, err := Exists(ctx, rfs, filepath.Join(dir, "nope", "bin")); err != nil || ok {
		t.Fatalf("missing parent: ok=%v err=%v", ok, err)
	}

	if err := VerifyRun(ctx, remotefstest.LocalRunner{}, "true"); err != nil {
		t.Errorf("true should verify: %v", err)
	}
	err := VerifyRun(ctx, remotefstest.LocalRunner{}, "echo boom >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "exit 3") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("failing verify should carry exit code + stderr, got %v", err)
	}
}
