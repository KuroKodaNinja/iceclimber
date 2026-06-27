// Package python installs a relocatable CPython into the sandbox using
// python-build-standalone (PBS). Python is always relay-based (plan §5): Popo
// downloads a PBS build on its own network, extracts it locally (Go stdlib), and
// pushes the tree into the sandbox over a remotefs.FS — works on either transport.
package python

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

	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Config holds the installer's dependencies.
type Config struct {
	FS         remotefs.FS   // where the interpreter is written (transport-agnostic)
	Runner     remote.Runner // used to verify the installed binary actually runs
	Root       string        // sandbox install root ($ICECLIMBER_ROOT)
	OS         string        // probe fingerprint (expects "linux")
	Arch       string        // "x86_64" | "aarch64"
	Libc       string        // "musl" | "glibc"
	CacheDir   string        // controller-side wheel/runtime cache
	HTTPClient *http.Client
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
	r, err := i.resolve(ctx, minor)
	if err != nil {
		return Result{}, err
	}
	target := i.targetDir(r.FullVersion)
	bin := path.Join(target, "bin", "python3")

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
		return Result{}, fmt.Errorf("push python tree: %w", err)
	}
	if err := i.verify(ctx, bin); err != nil {
		return Result{}, fmt.Errorf("installed python failed to run: %w", err)
	}
	return Result{Version: r.FullVersion, Path: bin, AlreadyInstalled: false}, nil
}

// targetDir is runtimes/python/<full>-<arch>-<libc> under the install root (§3).
func (i *Installer) targetDir(full string) string {
	return path.Join(i.cfg.Root, "runtimes", "python", fmt.Sprintf("%s-%s-%s", full, i.cfg.Arch, i.cfg.Libc))
}

// exists reports whether p is present in the sandbox (via a List of its parent,
// avoiding a stat the ExecFS palette doesn't have).
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

// verify runs the freshly installed interpreter over the exec channel — the real
// proof that the sandbox can execute what Popo pushed (§2 scoping boundary).
func (i *Installer) verify(ctx context.Context, bin string) error {
	cmd := remote.ShellQuote(bin) + " -c " + remote.ShellQuote("import sys; print(sys.version)")
	res, err := i.cfg.Runner.Run(ctx, cmd, nil)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// download fetches the asset into CacheDir (keyed by name, shared across
// sandboxes by fingerprint, §8) and verifies its SHA256. A valid cached copy is
// reused without re-downloading.
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
		return dst, nil // cached and valid
	}

	body, err := i.httpGet(ctx, r.URL)
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
	if _, err := io.Copy(io.MultiWriter(out, h), body); err != nil {
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

// httpGet performs a GET and returns the response body (caller closes it).
func (i *Installer) httpGet(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := i.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, nil
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
