// Package python installs a relocatable CPython into the sandbox using
// python-build-standalone (PBS). Python is always relay-based (plan §5): Popo
// downloads a PBS build on its own network, extracts it locally (Go stdlib), and
// pushes the tree into the sandbox over a remotefs.FS — works on either transport.
package python

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

// Config holds the installer's dependencies.
type Config struct {
	FS         remotefs.FS   // where the interpreter is written (transport-agnostic)
	Runner     remote.Runner // used to verify the installed binary actually runs
	Root       string        // sandbox install root ($ICECLIMBER_HOME)
	OS         string        // probe fingerprint (expects "linux")
	Arch       string        // "x86_64" | "aarch64"
	Libc       string        // "musl" | "glibc"
	CacheDir   string        // controller-side wheel/runtime cache
	HTTPClient *http.Client
	Progress   progress.Func // optional operator-facing progress (nil = silent)
}

// Installer installs Python runtimes into one sandbox.
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

// Result is the outcome of an install (also the python.install response body).
type Result struct {
	Version          string `json:"version"` // exact patch, e.g. "3.12.13"
	Path             string `json:"path"`    // absolute path to bin/python3
	AlreadyInstalled bool   `json:"already_installed"`
}

// Install ensures the requested minor version (e.g. "3.12") is present and
// runnable in the sandbox, returning the absolute path to its bin/python3.
func (i *Installer) Install(ctx context.Context, minor string) (Result, error) {
	i.cfg.Progress.Phase("resolving")
	r, err := i.resolve(ctx, minor)
	if err != nil {
		return Result{}, err
	}
	target := i.targetDir(r.FullVersion)
	bin := path.Join(target, "bin", "python3")

	if ok, err := relay.Exists(ctx, i.cfg.FS, bin); err != nil {
		return Result{}, err
	} else if ok {
		return Result{Version: r.FullVersion, Path: bin, AlreadyInstalled: true}, nil
	}

	tarball, err := i.fetch(ctx, r)
	if err != nil {
		return Result{}, err
	}
	if err := i.extractAndPush(ctx, tarball, target); err != nil {
		return Result{}, fmt.Errorf("push python tree: %w", err)
	}
	i.cfg.Progress.Phase("verifying")
	if err := i.verify(ctx, bin); err != nil {
		return Result{}, fmt.Errorf("installed python failed to run: %w", err)
	}
	return Result{Version: r.FullVersion, Path: bin, AlreadyInstalled: false}, nil
}

// targetDir is runtimes/python/<full>-<arch>-<libc> under the install root (§3).
func (i *Installer) targetDir(full string) string {
	return path.Join(i.cfg.Root, "runtimes", "python", fmt.Sprintf("%s-%s-%s", full, i.cfg.Arch, i.cfg.Libc))
}

// fetch downloads the resolved asset through the shared relay cache (per-asset flock;
// sha256 verified on cache hits and downloads alike).
func (i *Installer) fetch(ctx context.Context, r resolved) (string, error) {
	f := &relay.Fetcher{CacheDir: i.cfg.CacheDir, Client: i.cfg.HTTPClient, Progress: i.cfg.Progress}
	return f.Fetch(ctx, relay.Asset{Name: r.AssetName, URL: r.URL, SHA256: r.SHA256})
}

// exists reports whether p is present in the sandbox (via a List of its parent,
// avoiding a stat the ExecFS palette doesn't have).
// verify runs the freshly installed interpreter over the exec channel — the real
// proof that the sandbox can execute what Popo pushed (§2 scoping boundary).
func (i *Installer) verify(ctx context.Context, bin string) error {
	return relay.VerifyRun(ctx, i.cfg.Runner,
		remote.ShellQuote(bin)+" -c "+remote.ShellQuote("import sys; print(sys.version)"))
}

// httpGet performs a GET and returns the response body (caller closes it) and the
// Content-Length (-1 if unknown — used to drive download progress).
