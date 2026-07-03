// Package node installs a relocatable Node.js runtime into the sandbox, the
// second language after Python (plan §5, decision #22). Like Python it is always
// relay-based: Popo downloads an official Node distribution on its own network
// (glibc from nodejs.org/dist, musl from the unofficial-builds mirror), extracts
// it locally with the Go stdlib, and pushes the tree into the sandbox over a
// remotefs.FS. npm ships bundled in the tarball (bin/npm).
package node

import (
	"context"
	"fmt"
	"net/http"
	"path"

	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/relay"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Config holds the installer's dependencies (mirrors python.Config).
type Config struct {
	FS         remotefs.FS
	Runner     remote.Runner
	Root       string
	OS         string // probe fingerprint (expects "linux")
	Arch       string // "x86_64" | "aarch64"
	Libc       string // "musl" | "glibc"
	CacheDir   string
	HTTPClient *http.Client
	Progress   progress.Func
}

// Installer installs Node runtimes into one sandbox.
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

// Result is the outcome of an install (also the node.install response body).
type Result struct {
	Version          string `json:"version"` // exact, e.g. "20.11.1"
	Path             string `json:"path"`    // absolute path to bin/node
	AlreadyInstalled bool   `json:"already_installed"`
}

// Install ensures the requested version line (e.g. "20" or "20.11") is present and
// runnable in the sandbox, returning the absolute path to its bin/node.
func (i *Installer) Install(ctx context.Context, version string) (Result, error) {
	i.cfg.Progress.Phase("resolving")
	r, err := i.resolve(ctx, version)
	if err != nil {
		return Result{}, err
	}
	target := i.targetDir(r.FullVersion)
	bin := path.Join(target, "bin", "node")

	if ok, err := relay.Exists(ctx, i.cfg.FS, bin); err != nil {
		return Result{}, err
	} else if ok {
		return Result{Version: r.FullVersion, Path: bin, AlreadyInstalled: true}, nil
	}

	tarball, err := i.fetch(ctx, r)
	if err != nil {
		return Result{}, err
	}
	// Node tarballs nest everything under one top-level dir, which the shared push
	// strips.
	if err := i.extractAndPush(ctx, tarball, target); err != nil {
		return Result{}, fmt.Errorf("push node tree: %w", err)
	}
	i.cfg.Progress.Phase("verifying")
	if err := i.verify(ctx, bin); err != nil {
		return Result{}, fmt.Errorf("installed node failed to run: %w", err)
	}
	return Result{Version: r.FullVersion, Path: bin, AlreadyInstalled: false}, nil
}

// targetDir is runtimes/node/<full>-<arch>-<libc> under the install root (§3).
func (i *Installer) targetDir(full string) string {
	return path.Join(i.cfg.Root, "runtimes", "node", fmt.Sprintf("%s-%s-%s", full, i.cfg.Arch, i.cfg.Libc))
}

// fetch downloads the resolved asset through the shared relay cache (per-asset flock;
// sha256 verified on cache hits and downloads alike).
func (i *Installer) fetch(ctx context.Context, r resolved) (string, error) {
	f := &relay.Fetcher{CacheDir: i.cfg.CacheDir, Client: i.cfg.HTTPClient, Progress: i.cfg.Progress}
	return f.Fetch(ctx, relay.Asset{Name: r.AssetName, URL: r.URL, SHA256: r.SHA256})
}

// verify runs the freshly installed node over the exec channel.
func (i *Installer) verify(ctx context.Context, bin string) error {
	return relay.VerifyRun(ctx, i.cfg.Runner, remote.ShellQuote(bin)+" --version")
}

