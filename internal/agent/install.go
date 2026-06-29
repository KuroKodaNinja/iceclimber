package agent

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// defaultRegistry is the npm registry the controller downloads agent packages from.
const defaultRegistry = "https://registry.npmjs.org"

// Config holds the installer's dependencies.
type Config struct {
	FS         remotefs.FS
	Runner     remote.Runner
	Root       string
	OS         string
	Arch       string
	Libc       string
	CacheDir   string
	Registry   string // controller npm registry base (default registry.npmjs.org)
	HTTPClient *http.Client
}

// Installer installs agents into one sandbox by relaying their native binary in.
type Installer struct{ cfg Config }

// NewInstaller builds an agent installer.
func NewInstaller(cfg Config) *Installer {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.Registry == "" {
		cfg.Registry = defaultRegistry
	}
	return &Installer{cfg: cfg}
}

// Result is the outcome of an agent install.
type Result struct {
	Agent          string `json:"agent"`
	Version        string `json:"version"`
	Bin            string `json:"bin"`      // absolute path to the agent binary in the sandbox
	Dir            string `json:"dir"`      // <root>/agent/<name> — the agent's home, on PATH
	EnvFile        string `json:"env_file"` // 0600 env file (empty if --skip-auth)
	AuthConfigured bool   `json:"auth_configured"`
}

// Install downloads the agent's per-platform package on the controller, relays its
// native binary into the sandbox (the bulk tar push — no on-target npm, no Node),
// writes the auth env file (unless token is empty), and verifies the binary runs.
// The token is never logged; it reaches the sandbox only as 0600 file content.
func (i *Installer) Install(ctx context.Context, d Descriptor, token string) (Result, error) {
	pkg, err := d.PlatformPackage(i.cfg.OS, i.cfg.Arch, i.cfg.Libc)
	if err != nil {
		return Result{}, err
	}
	version, tarball, integrity, err := i.resolve(ctx, pkg)
	if err != nil {
		return Result{}, fmt.Errorf("resolve %s: %w", pkg, err)
	}
	tgz, err := i.download(ctx, pkg, version, tarball, integrity)
	if err != nil {
		return Result{}, fmt.Errorf("download %s: %w", pkg, err)
	}

	// Relay the package tree in: PushTarGz strips the "package/" root, so the binary
	// lands at <dir>/<BinaryPath> with its executable bit preserved (bulk tar on
	// ExecFS; per-file on SFTP). The sandbox never touches the registry.
	dir := path.Join(i.cfg.Root, "agent", d.Name)
	f, err := os.Open(tgz)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	if err := remotefs.PushTarGz(ctx, i.cfg.FS, f, dir); err != nil {
		return Result{}, fmt.Errorf("relay %s into sandbox: %w", d.DisplayName, err)
	}
	binPath := path.Join(dir, d.BinaryPath)
	if err := i.cfg.FS.Chmod(ctx, binPath, 0o755); err != nil {
		return Result{}, fmt.Errorf("chmod agent binary: %w", err)
	}

	res := Result{Agent: d.Name, Version: version, Bin: binPath, Dir: dir}

	if token != "" {
		envFile := path.Join(dir, "env.sh")
		if err := i.writeSecret(ctx, envFile, renderEnv(d, token, dir)); err != nil {
			return Result{}, fmt.Errorf("write agent env: %w", err)
		}
		res.EnvFile = envFile
		res.AuthConfigured = true
	}

	if err := i.verify(ctx, binPath, d); err != nil {
		return res, fmt.Errorf("installed %s failed to run: %w", d.Bin, err)
	}
	return res, nil
}

// npmDist is the slim packument's per-version dist block.
type npmDist struct {
	Tarball   string `json:"tarball"`
	Integrity string `json:"integrity"`
}

// resolve fetches the package's latest version, tarball URL, and integrity from the
// registry (the abbreviated packument).
func (i *Installer) resolve(ctx context.Context, pkg string) (version, tarball, integrity string, err error) {
	url := i.cfg.Registry + "/" + strings.Replace(pkg, "/", "%2F", 1)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Accept", "application/vnd.npm.install-v1+json")
	resp, err := i.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	var doc struct {
		DistTags map[string]string                 `json:"dist-tags"`
		Versions map[string]struct{ Dist npmDist } `json:"versions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", "", "", err
	}
	latest := doc.DistTags["latest"]
	if latest == "" {
		return "", "", "", fmt.Errorf("no latest dist-tag")
	}
	v, ok := doc.Versions[latest]
	if !ok || v.Dist.Tarball == "" {
		return "", "", "", fmt.Errorf("no tarball for %s@%s", pkg, latest)
	}
	return latest, v.Dist.Tarball, v.Dist.Integrity, nil
}

// download fetches the tarball into CacheDir (keyed by pkg+version), verifying its
// sha512 integrity, and reuses a valid cached copy.
func (i *Installer) download(ctx context.Context, pkg, version, tarball, integrity string) (string, error) {
	cache := i.cfg.CacheDir
	if cache == "" {
		cache = filepath.Join(os.TempDir(), "iceclimber-cache")
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(cache, strings.ReplaceAll(pkg, "/", "_")+"-"+version+".tgz")
	if verifyIntegrity(dst, integrity) == nil {
		return dst, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tarball, nil)
	if err != nil {
		return "", err
	}
	resp, err := i.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", tarball, resp.Status)
	}
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(tmp)
		return "", err
	}
	out.Close()
	if err := verifyIntegrity(tmp, integrity); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// verifyIntegrity checks a file against an npm "sha512-<base64>" integrity string.
func verifyIntegrity(file, integrity string) error {
	b64, ok := strings.CutPrefix(integrity, "sha512-")
	if !ok {
		return fmt.Errorf("unsupported integrity %q", integrity)
	}
	want, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return fmt.Errorf("decode integrity: %w", err)
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := h.Sum(nil); !bytes.Equal(got, want) {
		return fmt.Errorf("integrity mismatch for %s", file)
	}
	return nil
}

// renderEnv builds the agent's env file: the subscription token, the API-key var
// blanked (never fall back to metered billing), the agent's extra env, and the
// agent's dir on PATH so its binary is runnable by name.
func renderEnv(d Descriptor, token, dir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# iceclimber: %s agent environment (operator-written secret; chmod 600, do not commit).\n", d.DisplayName)
	fmt.Fprintf(&b, "export %s=%s\n", d.TokenEnv, remote.ShellQuote(token))
	if d.APIKeyEnv != "" {
		fmt.Fprintf(&b, "export %s=\n", d.APIKeyEnv)
	}
	for _, e := range d.Env {
		fmt.Fprintf(&b, "export %s=%s\n", e.Key, remote.ShellQuote(e.Value))
	}
	fmt.Fprintf(&b, "export PATH=%s:\"$PATH\"\n", remote.ShellQuote(dir))
	return b.String()
}

func (i *Installer) writeSecret(ctx context.Context, p, content string) error {
	if err := i.cfg.FS.Mkdir(ctx, path.Dir(p)); err != nil {
		return err
	}
	if err := i.cfg.FS.WriteFile(ctx, p, []byte(content)); err != nil {
		return err
	}
	return i.cfg.FS.Chmod(ctx, p, 0o600)
}

func (i *Installer) verify(ctx context.Context, bin string, d Descriptor) error {
	cmd := remote.ShellQuote(bin)
	for _, a := range d.VersionArgs {
		cmd += " " + remote.ShellQuote(a)
	}
	res, err := i.cfg.Runner.Run(ctx, cmd, nil)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}
