package catalog

import (
	"bytes"
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

// InstallDeps is what installing a tool needs from an open session (the registry fills it,
// mirroring the runtime installers' Config).
type InstallDeps struct {
	FS       remotefs.FS
	Runner   remote.Runner
	Root     string
	OS       string
	Arch     string
	Libc     string
	CacheDir string
	Client   *http.Client
	Progress progress.Func
}

// Result is the outcome of installing a tool.
type Result struct {
	Name             string   `json:"name"`
	Version          string   `json:"version"`
	Path             string   `json:"path"` // the first installed bin's absolute path
	Bins             []string `json:"bins"` // absolute paths of every linked bin
	AlreadyInstalled bool     `json:"already_installed"`
}

// ToolsBinDir is the single PATH directory every tool's bins are linked into.
func ToolsBinDir(root string) string { return path.Join(root, "tools", "bin") }

// Install downloads (via the shared relay cache), pushes, links, and verifies the tool for
// the sandbox fingerprint. Idempotent: a present, verifying install is left as-is. On a
// version change the install dir is replaced wholesale (an iceclimber-owned dir). Returns
// the linked bins under <root>/tools/bin.
func (e Entry) Install(ctx context.Context, d InstallDeps) (Result, error) {
	d.Progress.Phase("resolving")
	r, err := e.Resolve(ctx, d.Client, d.OS, d.Arch, d.Libc)
	if err != nil {
		return Result{}, err
	}
	dest := e.installDest(d.Root)
	binDir := ToolsBinDir(d.Root)
	verFile := path.Join(dest, versionMarker)

	// Idempotency keyed on the installed VERSION, not mere presence: `verify` (e.g.
	// "jq --version") passes for ANY version, so a presence check would skip an upgrade.
	// A version marker written at install time is the source of truth; the same version
	// still installed + verifying → nothing to do. A different (or absent) marker → install.
	if cur, err := d.FS.ReadFile(ctx, verFile); err == nil && string(cur) == e.Version {
		if err := e.verify(ctx, d.Runner, binDir); err == nil {
			return e.result(binDir, true), nil
		}
		// Marker matches but doesn't verify (corrupt/partial) → reinstall over it.
	}

	cached, err := (&relay.Fetcher{CacheDir: d.CacheDir, Client: d.Client, Progress: d.Progress}).Fetch(ctx, r.Asset)
	if err != nil {
		return Result{}, err
	}
	data, err := os.ReadFile(cached)
	if err != nil {
		return Result{}, err
	}

	d.Progress.Phase("transferring")
	// Replace the install dir wholesale so an upgrade never leaves stale files behind.
	_ = d.FS.RemoveAll(ctx, dest)
	if err := d.FS.Mkdir(ctx, dest); err != nil {
		return Result{}, fmt.Errorf("create %s: %w", dest, err)
	}
	if err := e.place(ctx, d.FS, dest, data); err != nil {
		return Result{}, err
	}

	// Link each declared bin into the single tools/bin PATH dir. (Bin-name uniqueness
	// across tools is enforced at catalog Load — a conflict never reaches here.)
	if err := d.FS.Mkdir(ctx, binDir); err != nil {
		return Result{}, err
	}
	for _, b := range e.Unpack.Bins {
		target := path.Join(dest, b)
		link := path.Join(binDir, b)
		if err := d.FS.Chmod(ctx, target, 0o755); err != nil {
			return Result{}, fmt.Errorf("chmod %s: %w", b, err)
		}
		// Symlink is idempotent in remotefs (re-points), so an upgrade re-links cleanly.
		if err := d.FS.Symlink(ctx, target, link); err != nil {
			return Result{}, fmt.Errorf("link %s into tools/bin: %w", b, err)
		}
	}

	// Stamp the installed version (the idempotency/upgrade source of truth) before verify,
	// so a re-run after a transient verify failure still sees the right version.
	if err := d.FS.WriteFile(ctx, verFile, []byte(e.Version)); err != nil {
		return Result{}, fmt.Errorf("write version marker: %w", err)
	}

	d.Progress.Phase("verifying")
	if err := e.verify(ctx, d.Runner, binDir); err != nil {
		return Result{}, fmt.Errorf("installed %s failed to run: %w", e.Name, err)
	}
	return e.result(binDir, false), nil
}

// versionMarker is the file under an install dest recording the installed version.
const versionMarker = ".iceclimber-tool-version"

// place writes the fetched asset into dest per unpack.format: a single binary, or a
// strip-1 tar.gz tree (via the shared PushTarGz — both transports, exec-bit preserved).
func (e Entry) place(ctx context.Context, fs remotefs.FS, dest string, data []byte) error {
	switch e.Unpack.Format {
	case "binary":
		// A single binary: name it after its (sole) declared bin.
		return fs.WriteFile(ctx, path.Join(dest, e.Unpack.Bins[0]), data)
	case "tar.gz":
		return remotefs.PushTarGz(ctx, fs, bytes.NewReader(data), dest)
	default:
		return fmt.Errorf("tool %q: unknown unpack.format %q", e.Name, e.Unpack.Format)
	}
}

// verify runs the entry's verify command with the tools/bin dir on PATH, so the command
// (e.g. "jq --version") resolves the freshly-linked bin by name.
func (e Entry) verify(ctx context.Context, r remote.Runner, binDir string) error {
	cmd := `PATH=` + remote.ShellQuote(binDir) + `:"$PATH" ` + e.Verify
	return relay.VerifyRun(ctx, r, cmd)
}

func (e Entry) result(binDir string, already bool) Result {
	bins := make([]string, len(e.Unpack.Bins))
	for i, b := range e.Unpack.Bins {
		bins[i] = path.Join(binDir, b)
	}
	return Result{Name: e.Name, Version: e.Version, Path: bins[0], Bins: bins, AlreadyInstalled: already}
}
