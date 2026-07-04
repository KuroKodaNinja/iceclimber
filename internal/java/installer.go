// Package java installs a relocatable Temurin JDK into the sandbox, the third
// language after Python and Node (plan §5, decision #22). Like the others it is
// always relay-based: Popo downloads an official Adoptium build on its own network
// (glibc = os "linux", musl = os "alpine-linux"), verifies its SHA256, extracts it
// locally with the Go stdlib, and pushes the tree into the sandbox over a
// remotefs.FS. The tarball is .tar.gz, so the gzip stream-push carries over with no
// xz dependency. javac ships in the JDK (bin/javac).
package java

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

// Config holds the installer's dependencies (mirrors node.Config / python.Config).
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
	// Pin (optional) short-circuits resolve() with an operator-pinned exact asset for a
	// requested version, skipping upstream metadata calls; nil or a false return falls
	// through to the live resolver. Built from config runtime_pins (§17).
	Pin func(version string) (relay.Pin, bool)
}

// Installer installs JDK runtimes into one sandbox.
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

// Result is the outcome of an install (also the java.install response body).
type Result struct {
	Version          string `json:"version"` // exact, e.g. "21.0.11+10"
	Path             string `json:"path"`    // absolute path to bin/java
	AlreadyInstalled bool   `json:"already_installed"`
}

// Install ensures the requested feature version (e.g. "21" or "17") is present and
// runnable in the sandbox, returning the absolute path to its bin/java.
func (i *Installer) Install(ctx context.Context, version string) (Result, error) {
	i.cfg.Progress.Phase("resolving")
	r, err := i.resolve(ctx, version)
	if err != nil {
		return Result{}, err
	}
	target := i.targetDir(r.FullVersion)
	bin := path.Join(target, "bin", "java")

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
		return Result{}, fmt.Errorf("push jdk tree: %w", err)
	}
	i.cfg.Progress.Phase("verifying")
	if err := i.verify(ctx, bin); err != nil {
		return Result{}, fmt.Errorf("installed jdk failed to run: %w", err)
	}
	return Result{Version: r.FullVersion, Path: bin, AlreadyInstalled: false}, nil
}

// targetDir is runtimes/java/<full>-<arch>-<libc> under the install root (§3). The
// full version may carry a "+build" suffix (e.g. "21.0.11+10"); that's a valid
// directory name on Linux.
func (i *Installer) targetDir(full string) string {
	return path.Join(i.cfg.Root, "runtimes", "java", fmt.Sprintf("%s-%s-%s", full, i.cfg.Arch, i.cfg.Libc))
}

// fetch downloads the resolved asset through the shared relay cache (per-asset flock;
// sha256 verified on cache hits and downloads alike).
func (i *Installer) fetch(ctx context.Context, r resolved) (string, error) {
	f := &relay.Fetcher{CacheDir: i.cfg.CacheDir, Client: i.cfg.HTTPClient, Progress: i.cfg.Progress}
	return f.Fetch(ctx, relay.Asset{Name: r.AssetName, URL: r.URL, SHA256: r.SHA256})
}

// verify runs the freshly installed java over the exec channel. `java -version`
// prints to stderr and exits 0; we only check the exit code.
func (i *Installer) verify(ctx context.Context, bin string) error {
	return relay.VerifyRun(ctx, i.cfg.Runner, remote.ShellQuote(bin)+" -version")
}
