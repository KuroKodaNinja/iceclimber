// Package shell relays a portable interactive shell (a fully-static bash) into the
// sandbox, so an operator who `iceclimber shell`s in gets history + tab-completion and a
// sane PATH instead of the box's bare BusyBox ash. Like the language runtimes it is
// relay-based and air-gap-friendly: Popo downloads a pinned, checksum-verified static bash
// on its own network and pushes the single binary into the sandbox over a remotefs.FS,
// then verifies it runs. bash's built-in readline provides history and completion with no
// extra bundle, and the musl-static build runs on both musl and glibc boxes.
package shell

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

	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Config holds the installer's dependencies (mirrors node.Config / python.Config).
type Config struct {
	FS         remotefs.FS
	Runner     remote.Runner
	Root       string
	OS         string // probe fingerprint (expects "linux")
	Arch       string // "x86_64" | "aarch64"
	Libc       string // "musl" | "glibc" — recorded for symmetry; the static build covers both
	CacheDir   string
	HTTPClient *http.Client
	Progress   progress.Func
}

// Installer installs the interactive shell into one sandbox.
type Installer struct {
	cfg Config
}

// NewInstaller builds an installer.
func NewInstaller(cfg Config) *Installer {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Installer{cfg: cfg}
}

// Result is the outcome of an install.
type Result struct {
	Version          string `json:"version"` // e.g. "5.2.15"
	Path             string `json:"path"`    // absolute path to the installed bash
	RC               string `json:"rc"`      // absolute path to the dropped rc file
	AlreadyInstalled bool   `json:"already_installed"`
}

// Install ensures a portable bash is present and runnable at <root>/shell/bash, dropping its
// rc file alongside, and returns the absolute path. Idempotent: a present bash is left as-is
// (the rc is refreshed so an updated launcher env always lands).
func (i *Installer) Install(ctx context.Context) (Result, error) {
	i.cfg.Progress.Phase("resolving")
	r, err := i.resolve()
	if err != nil {
		return Result{}, err
	}
	bin := BinPath(i.cfg.Root)
	rc := RCPath(i.cfg.Root)

	if ok, err := i.exists(ctx, bin); err != nil {
		return Result{}, err
	} else if ok {
		// Already installed — but refresh the rc so a newer env preamble (e.g. more runtime
		// bins, egress changes) takes effect without a reinstall.
		if err := i.cfg.FS.WriteFile(ctx, rc, []byte(renderRC(i.cfg.Root))); err != nil {
			return Result{}, fmt.Errorf("refresh rc: %w", err)
		}
		return Result{Version: r.Version, Path: bin, RC: rc, AlreadyInstalled: true}, nil
	}

	cached, err := i.download(ctx, r)
	if err != nil {
		return Result{}, err
	}
	data, err := os.ReadFile(cached)
	if err != nil {
		return Result{}, err
	}
	i.cfg.Progress.Phase("transferring")
	if err := i.cfg.FS.Mkdir(ctx, path.Dir(bin)); err != nil {
		return Result{}, fmt.Errorf("create shell dir: %w", err)
	}
	if err := i.cfg.FS.WriteFile(ctx, bin, data); err != nil {
		return Result{}, fmt.Errorf("push bash: %w", err)
	}
	if err := i.cfg.FS.Chmod(ctx, bin, 0o755); err != nil {
		return Result{}, fmt.Errorf("chmod bash: %w", err)
	}
	if err := i.cfg.FS.WriteFile(ctx, rc, []byte(renderRC(i.cfg.Root))); err != nil {
		return Result{}, fmt.Errorf("write rc: %w", err)
	}
	i.cfg.Progress.Phase("verifying")
	if err := i.verify(ctx, bin); err != nil {
		return Result{}, fmt.Errorf("installed bash failed to run: %w", err)
	}
	return Result{Version: r.Version, Path: bin, RC: rc, AlreadyInstalled: false}, nil
}

func (i *Installer) exists(ctx context.Context, p string) (bool, error) {
	names, err := i.cfg.FS.List(ctx, path.Dir(p))
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

// verify runs the freshly installed bash over the exec channel.
func (i *Installer) verify(ctx context.Context, bin string) error {
	res, err := i.cfg.Runner.Run(ctx, remote.ShellQuote(bin)+" --version", nil)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// download fetches the asset into CacheDir (keyed by name) and verifies its SHA256, reusing a
// valid cached copy. Mirrors the runtime installers' cache-and-verify.
func (i *Installer) download(ctx context.Context, r resolved) (string, error) {
	cache := i.cfg.CacheDir
	if cache == "" {
		cache = filepath.Join(os.TempDir(), "iceclimber-cache")
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(cache, r.AssetName)
	if sum, err := fileSHA256(dst); err == nil && sum == r.SHA256 {
		return dst, nil
	}

	body, length, err := i.httpGet(ctx, r.URL)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", r.AssetName, err)
	}
	defer body.Close()

	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	src := i.cfg.Progress.Reader(body, "downloading", length)
	if _, err := io.Copy(io.MultiWriter(out, h), src); err != nil {
		out.Close()
		os.Remove(tmp)
		return "", err
	}
	out.Close()
	if got := hex.EncodeToString(h.Sum(nil)); got != r.SHA256 {
		os.Remove(tmp)
		return "", fmt.Errorf("sha256 mismatch for %s: got %s, want %s", r.AssetName, got, r.SHA256)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func (i *Installer) httpGet(ctx context.Context, url string) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := i.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, resp.ContentLength, nil
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
