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
	"fmt"
	"net/http"
	"os"
	"path"

	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/relay"
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

	if ok, err := relay.Exists(ctx, i.cfg.FS, bin); err != nil {
		return Result{}, err
	} else if ok {
		// Already installed — but refresh the rc so a newer env preamble (e.g. more runtime
		// bins, egress changes) takes effect without a reinstall.
		if err := i.cfg.FS.WriteFile(ctx, rc, []byte(renderRC(i.cfg.Root))); err != nil {
			return Result{}, fmt.Errorf("refresh rc: %w", err)
		}
		return Result{Version: r.Version, Path: bin, RC: rc, AlreadyInstalled: true}, nil
	}

	cached, err := i.fetch(ctx, r)
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

// fetch downloads the resolved asset through the shared relay cache (per-asset flock;
// sha256 verified on cache hits and downloads alike).
func (i *Installer) fetch(ctx context.Context, r resolved) (string, error) {
	f := &relay.Fetcher{CacheDir: i.cfg.CacheDir, Client: i.cfg.HTTPClient, Progress: i.cfg.Progress}
	return f.Fetch(ctx, relay.Asset{Name: r.AssetName, URL: r.URL, SHA256: r.SHA256})
}

// verify runs the freshly installed bash over the exec channel.
func (i *Installer) verify(ctx context.Context, bin string) error {
	return relay.VerifyRun(ctx, i.cfg.Runner, remote.ShellQuote(bin)+" --version")
}
