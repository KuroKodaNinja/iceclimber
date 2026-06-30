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
	Libc       string // "musl" | "glibc"
	CacheDir   string
	HTTPClient *http.Client
	Progress   progress.Func
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

	if ok, err := i.exists(ctx, bin); err != nil {
		return Result{}, err
	} else if ok {
		return Result{Version: r.FullVersion, Path: bin, AlreadyInstalled: true}, nil
	}

	tarball, err := i.download(ctx, r)
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

// verify runs the freshly installed java over the exec channel. `java -version`
// prints to stderr and exits 0; we only check the exit code.
func (i *Installer) verify(ctx context.Context, bin string) error {
	res, err := i.cfg.Runner.Run(ctx, remote.ShellQuote(bin)+" -version", nil)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// download fetches the asset into CacheDir (keyed by name) and verifies its
// SHA256, reusing a valid cached copy.
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
