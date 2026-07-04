// Package runtime is the shared install engine for the managed language runtimes
// (python, node, java — and any future language). The three installers were byte-identical
// except a subdir name, a bin name, a verify command, and their resolve() upstream; that
// orchestration — resolve (with the operator-pin short-circuit) → idempotency/verify-on-hit →
// relay fetch → replace-and-push → verify — lives here ONCE, parameterized by a per-language
// Spec. Adding a language is then a resolve() + a Spec literal, not a copy of this shell (§ the
// pre-fleet encapsulation refactor). Everything load-bearing (download/sha256/cache/push) is
// internal/relay + internal/remotefs, exactly as before.
package runtime

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

// Fingerprint is the sandbox platform a resolve() targets.
type Fingerprint struct {
	OS   string // expects "linux"
	Arch string // "x86_64" | "aarch64"
	Libc string // "musl" | "glibc"
}

// Spec is a language's binding into the shared engine: where its tree lives, how to verify it
// runs, its default line, and its language-specific upstream resolver. The pin short-circuit is
// NOT part of a Spec — it's handled once by the engine (Config.Pin), so a resolve() only ever
// implements the live upstream path.
type Spec struct {
	Lang        string                                                                                            // runtimes/<Lang> subdir + the "<Lang>.install" verb
	BinName     string                                                                                            // bin/<BinName>, e.g. "python3"
	Recommended string                                                                                            // the line installed when the caller omits a version / says "latest"
	Verify      func(bin string) string                                                                           // the shell command that proves the install runs
	Resolve     func(ctx context.Context, fp Fingerprint, client *http.Client, version string) (relay.Pin, error) // live upstream resolution → the exact checksummed asset
	// Assemble (optional) transforms the fetched upstream tarball into a clean, ready-to-push
	// tree ON THE CONTROLLER before it's streamed to the sandbox — for runtimes whose
	// distribution isn't already a strip-1 bin/ layout. (Rust ships a component-split tarball —
	// cargo/, rustc/, rust-std-<target>/ each with their own subtree + an install.sh — that must
	// be merged into one prefix so rustc finds its sysroot.) It receives the local path to the
	// fetched tarball and returns the local path to a gzipped tar of the clean tree, wrapped in a
	// single top-level dir the push strips (exactly like the native layouts). nil (the common
	// case) pushes the tarball as-is.
	Assemble func(ctx context.Context, tarball string) (cleanTarball string, err error)
}

// Config holds one install's dependencies (identical across languages — the whole point).
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

// Installer installs one language's runtimes into one sandbox.
type Installer struct {
	cfg  Config
	spec Spec
}

// New builds an installer for a language Spec.
func New(spec Spec, cfg Config) *Installer {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Installer{cfg: cfg, spec: spec}
}

// Result is the outcome of an install (also the <lang>.install response body).
type Result struct {
	Version          string `json:"version"` // exact full version, e.g. "3.12.13"
	Path             string `json:"path"`    // absolute path to bin/<BinName>
	AlreadyInstalled bool   `json:"already_installed"`
}

// Install ensures the requested version line is present and runnable in the sandbox, returning
// the absolute path to its bin/<BinName>.
func (i *Installer) Install(ctx context.Context, version string) (Result, error) {
	i.cfg.Progress.Phase("resolving")
	pin, err := i.resolve(ctx, version)
	if err != nil {
		return Result{}, err
	}
	target := i.targetDir(pin.FullVersion)
	bin := path.Join(target, "bin", i.spec.BinName)

	if ok, err := relay.Exists(ctx, i.cfg.FS, bin); err != nil {
		return Result{}, err
	} else if ok {
		// Verify-on-hit: bin existing can be a PARTIAL install (interrupted mid-tree). Only a
		// clean run counts as installed; otherwise reinstall over the broken tree (heal).
		if err := i.verify(ctx, bin); err == nil {
			return Result{Version: pin.FullVersion, Path: bin, AlreadyInstalled: true}, nil
		}
	}

	tarball, err := i.fetch(ctx, pin)
	if err != nil {
		return Result{}, err
	}
	// A non-standard distribution (Rust's component split) is assembled into a clean strip-1
	// tree on the controller before it's pushed; the common case pushes the tarball as-is.
	if i.spec.Assemble != nil {
		i.cfg.Progress.Phase("assembling")
		clean, err := i.spec.Assemble(ctx, tarball)
		if err != nil {
			return Result{}, fmt.Errorf("assemble %s tree: %w", i.spec.Lang, err)
		}
		tarball = clean
	}
	// Replace the target wholesale so a repaired (partial) install leaves no stale files (a
	// fresh install's target doesn't exist, so this is a no-op). The runtime tarballs nest
	// everything under one top-level dir, which the shared push strips.
	_ = i.cfg.FS.RemoveAll(ctx, target)
	if err := i.extractAndPush(ctx, tarball, target); err != nil {
		return Result{}, fmt.Errorf("push %s tree: %w", i.spec.Lang, err)
	}
	i.cfg.Progress.Phase("verifying")
	if err := i.verify(ctx, bin); err != nil {
		return Result{}, fmt.Errorf("installed %s failed to run: %w", i.spec.Lang, err)
	}
	return Result{Version: pin.FullVersion, Path: bin, AlreadyInstalled: false}, nil
}

// resolve consults the operator pin first (the air-gapped-mirror short-circuit, §17) and falls
// through to the language's live upstream resolver on a miss.
func (i *Installer) resolve(ctx context.Context, version string) (relay.Pin, error) {
	if i.cfg.Pin != nil {
		if p, ok := i.cfg.Pin(version); ok {
			return p, nil
		}
	}
	return i.spec.Resolve(ctx, Fingerprint{OS: i.cfg.OS, Arch: i.cfg.Arch, Libc: i.cfg.Libc}, i.cfg.HTTPClient, version)
}

// targetDir is runtimes/<lang>/<full>-<arch>-<libc> under the install root (§3).
func (i *Installer) targetDir(full string) string {
	return path.Join(i.cfg.Root, "runtimes", i.spec.Lang, fmt.Sprintf("%s-%s-%s", full, i.cfg.Arch, i.cfg.Libc))
}

// fetch downloads the resolved asset through the shared relay cache (per-asset flock; sha256
// verified on cache hits and downloads alike).
func (i *Installer) fetch(ctx context.Context, pin relay.Pin) (string, error) {
	f := &relay.Fetcher{CacheDir: i.cfg.CacheDir, Client: i.cfg.HTTPClient, Progress: i.cfg.Progress}
	return f.Fetch(ctx, relay.Asset{Name: pin.AssetName, URL: pin.URL, SHA256: pin.SHA256})
}

// extractAndPush opens the cached tarball and streams it into target via the shared remotefs
// push (strips the top-level dir; bulk `tar` transfer on SFTP-less sandboxes).
func (i *Installer) extractAndPush(ctx context.Context, tarball, target string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	var total int64
	if st, err := f.Stat(); err == nil {
		total = st.Size()
	}
	src := i.cfg.Progress.Reader(f, "transferring", total)
	return remotefs.PushTarGz(ctx, i.cfg.FS, src, target)
}

// verify runs the freshly installed binary over the exec channel — the real proof the sandbox
// can execute what Popo pushed.
func (i *Installer) verify(ctx context.Context, bin string) error {
	return relay.VerifyRun(ctx, i.cfg.Runner, i.spec.Verify(bin))
}
