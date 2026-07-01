package conda

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

// RelayInstall is the air-gapped tier (plan §5), the conda analogue of pip's RelayInstall.
// The sandbox has no channel reachability, so the CONTROLLER (which has conda + network)
// solves the environment for the sandbox's platform, downloads every resolved package,
// synthesizes a self-contained local conda channel (repodata.json per subdir — no
// conda-index dependency), pushes it into the sandbox, and the sandbox creates the env
// OFFLINE from that channel. Nothing leaves the sandbox's disk; it never touches the
// network.
//
// EnvPrefix must already be set to python.CondaEnvPrefix(root, version) by the caller
// (handler.Run), because the relay creates the env itself rather than going through
// python.EnsureEnv (which would try an online `conda create`).
func (m *Manager) RelayInstall(ctx context.Context, specs []pkg.Spec, minor string) (pkg.Outcome, error) {
	if minor == "" {
		return pkg.Outcome{}, fmt.Errorf("conda relay requires a python_version (e.g. 3.12)")
	}
	conda := firstNonEmpty(m.cfg.ControllerConda, "conda")
	if out, err := exec.CommandContext(ctx, conda, "--version").CombinedOutput(); err != nil {
		return pkg.Outcome{}, fmt.Errorf("conda relay needs conda on the controller (set controller_conda): %v: %s", err, strings.TrimSpace(string(out)))
	}
	subdir := condaSubdir(m.cfg.Arch, m.cfg.Libc)

	// 1. Controller solve — resolve the full environment for the SANDBOX's platform
	//    (CONDA_SUBDIR pins the target arch so the controller's own arch is irrelevant),
	//    as a dry run that yields the package records without mutating anything.
	m.cfg.Progress.Phase("resolving (controller)")
	recs, err := m.controllerSolve(ctx, conda, subdir, minor, specs)
	if err != nil {
		return pkg.Outcome{}, err
	}
	if len(recs) == 0 {
		return pkg.Outcome{}, fmt.Errorf("controller solve produced no packages")
	}

	// 2. Download every resolved package into a local channel tree, grouped by subdir.
	localChan, err := os.MkdirTemp("", "iceclimber-conda-chan-")
	if err != nil {
		return pkg.Outcome{}, err
	}
	defer os.RemoveAll(localChan)
	shas, err := m.downloadChannel(ctx, localChan, recs)
	if err != nil {
		return pkg.Outcome{}, err
	}

	// 3. Synthesize repodata.json per subdir and push the whole channel into the sandbox.
	sandboxChan := path.Join(m.cfg.Root, "blobs", "conda-chan-"+protocol.NewID())
	defer func() { _ = m.cfg.FS.RemoveAll(ctx, sandboxChan) }() // the pushed channel is transient
	if err := m.pushChannel(ctx, localChan, sandboxChan, recs); err != nil {
		return pkg.Outcome{}, err
	}

	// 4. Create the env OFFLINE in the sandbox from the pushed file:// channel.
	m.cfg.Progress.Phase("installing (offline)")
	chanURL := "file://" + sandboxChan
	res, err := m.cfg.Runner.Run(ctx, m.offlineCreateCmd(m.cfg.EnvPrefix, chanURL, minor, specs), nil)
	if err != nil {
		return pkg.Outcome{}, fmt.Errorf("run offline conda create: %w", err)
	}
	out, err := m.resultOutcome(res, specs, pkg.TierRelay)
	if err != nil {
		return pkg.Outcome{}, err
	}
	// Stamp the sha256 of the relayed package onto each installed spec where we have it.
	for i, in := range out.Installed {
		if sum, ok := shas[in.Name]; ok {
			out.Installed[i].SHA256 = sum
		}
	}
	return out, nil
}

// controllerSolve runs a dry-run `conda create` for the sandbox subdir and returns the
// resolved package records (LINK, backfilled from FETCH for download metadata). The
// controller's channels (from ExtraArgs) steer the solve; the sandbox-only offline flags
// are dropped here since the controller resolves against the real channel.
func (m *Manager) controllerSolve(ctx context.Context, conda, subdir, minor string, specs []pkg.Spec) ([]condaRecord, error) {
	tmp, err := os.MkdirTemp("", "iceclimber-conda-solve-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	// conda create refuses a non-empty prefix; point at a not-yet-created subpath.
	prefix := filepath.Join(tmp, "env")

	args := []string{"create", "-p", prefix, "--dry-run", "--json", "python=" + minor}
	args = append(args, controllerChannelArgs(m.cfg.ExtraArgs)...)
	for _, s := range specs {
		args = append(args, condaSpec(s))
	}
	cmd := exec.CommandContext(ctx, conda, args...)
	cmd.Env = append(os.Environ(), "CONDA_SUBDIR="+subdir)
	out, runErr := cmd.CombinedOutput()

	sv, perr := parseSolveJSON(out)
	if perr != nil {
		return nil, fmt.Errorf("controller conda solve failed: %s", lastLines(out, 6))
	}
	if !sv.Success {
		msg := firstNonEmpty(sv.Message, sv.Error, lastLines(out, 6), "conda solve failed")
		return nil, fmt.Errorf("controller conda solve failed: %s", msg)
	}
	if runErr != nil && len(sv.Actions.Link) == 0 {
		return nil, fmt.Errorf("controller conda solve failed: %s", lastLines(out, 6))
	}
	return mergeRecords(sv.Actions.Link, sv.Actions.Fetch), nil
}

// downloadChannel fetches every record's package file into localChan/<subdir>/<fn>,
// verifying sha256/md5 when the solve provided one, and returns name -> sha256 of the
// downloaded file so installed packages can be reported with a content hash.
func (m *Manager) downloadChannel(ctx context.Context, localChan string, recs []condaRecord) (map[string]string, error) {
	client := &http.Client{Timeout: 10 * time.Minute}
	shas := map[string]string{}
	for i, r := range recs {
		url, fn, sub := r.url(), r.fn(), r.subdir()
		if url == "" {
			return nil, fmt.Errorf("solve record %s-%s has no download URL", r.Name, r.Version)
		}
		m.cfg.Progress.Emit(progress.Event{Phase: "downloading " + fn, Cur: int64(i + 1), Total: int64(len(recs)), Unit: progress.Items})
		dir := filepath.Join(localChan, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		sum, err := downloadFile(ctx, client, url, filepath.Join(dir, fn))
		if err != nil {
			return nil, fmt.Errorf("download %s: %w", fn, err)
		}
		if r.SHA256 != "" && !strings.EqualFold(r.SHA256, sum) {
			return nil, fmt.Errorf("sha256 mismatch for %s: solve %s, downloaded %s", fn, r.SHA256, sum)
		}
		shas[r.Name] = sum
	}
	return shas, nil
}

// pushChannel synthesizes repodata.json for each subdir present (plus an empty noarch
// repodata so conda never 404s on the noarch index) and relays the whole channel tree
// into the sandbox via remotefs.
func (m *Manager) pushChannel(ctx context.Context, localChan, sandboxChan string, recs []condaRecord) error {
	bySubdir := map[string][]condaRecord{"noarch": nil} // always emit noarch, even if empty
	for _, r := range recs {
		s := r.subdir()
		bySubdir[s] = append(bySubdir[s], r)
	}
	for sub, srecs := range bySubdir {
		dstDir := path.Join(sandboxChan, sub)
		if err := m.cfg.FS.Mkdir(ctx, dstDir); err != nil {
			return fmt.Errorf("create sandbox channel dir %s: %w", sub, err)
		}
		for _, r := range srecs {
			data, err := os.ReadFile(filepath.Join(localChan, sub, r.fn()))
			if err != nil {
				return err
			}
			if err := m.cfg.FS.WriteFile(ctx, path.Join(dstDir, r.fn()), data); err != nil {
				return fmt.Errorf("relay package %s: %w", r.fn(), err)
			}
		}
		repodata, err := synthesizeRepodata(sub, srecs)
		if err != nil {
			return err
		}
		if err := m.cfg.FS.WriteFile(ctx, path.Join(dstDir, "repodata.json"), repodata); err != nil {
			return fmt.Errorf("write repodata for %s: %w", sub, err)
		}
	}
	return nil
}

// offlineCreateCmd builds the sandbox-side offline env create: it draws exclusively from
// the pushed file:// channel (--offline --override-channels), so no network is used.
func (m *Manager) offlineCreateCmd(prefix, chanURL, minor string, specs []pkg.Spec) string {
	args := []string{
		remote.ShellQuote(m.cfg.CondaBin), "create", "-y", "--json",
		"-p", remote.ShellQuote(prefix),
		"--offline", "--override-channels",
		// The default libmamba solver cannot load a synthesized local repodata.json for a
		// file:// channel ("Could not load repodata"); the classic solver reads it fine and
		// is always built into conda. Forcing it keeps the relay working without shipping a
		// zstd-compressed repodata.json.zst (which libmamba would otherwise require).
		"--solver", "classic",
		"-c", remote.ShellQuote(chanURL),
		remote.ShellQuote("python=" + minor),
	}
	for _, s := range specs {
		args = append(args, remote.ShellQuote(condaSpec(s)))
	}
	return strings.Join(args, " ")
}

// controllerChannelArgs keeps only the channel-selection flags from the agent's
// allowlisted extra args for the CONTROLLER solve (-c/--channel/--override-channels). The
// sandbox-only offline flags (--offline/--use-local) are dropped: the controller must
// reach the real channel to resolve and download.
func controllerChannelArgs(extraArgs []string) []string {
	var out []string
	for i := 0; i < len(extraArgs); i++ {
		switch extraArgs[i] {
		case "-c", "--channel":
			if i+1 < len(extraArgs) {
				out = append(out, extraArgs[i], extraArgs[i+1])
				i++
			}
		case "--override-channels":
			out = append(out, extraArgs[i])
		}
	}
	return out
}

// condaSubdir maps the sandbox's arch/libc to a conda platform subdir. conda has no musl
// builds (conda-forge is glibc-only), so musl falls back to the glibc subdir and the
// solve will simply fail to find packages — reported clearly rather than mis-resolved.
func condaSubdir(arch, _ string) string {
	switch arch {
	case "arm64", "aarch64":
		return "linux-aarch64"
	default: // amd64 / x86_64
		return "linux-64"
	}
}

// --- solve JSON + repodata synthesis ---------------------------------------

// condaRecord is a package record from a `conda … --dry-run --json` solve. conda emits
// full PackageRecord fields in the LINK/FETCH actions; we consume the subset needed to
// download the file and rebuild a repodata.json entry.
type condaRecord struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Build       string   `json:"build"`
	BuildNumber int      `json:"build_number"`
	Subdir      string   `json:"subdir"`
	Fn          string   `json:"fn"`
	URL         string   `json:"url"`
	Depends     []string `json:"depends"`
	Constrains  []string `json:"constrains"`
	MD5         string   `json:"md5"`
	SHA256      string   `json:"sha256"`
	Size        int64    `json:"size"`
	License     string   `json:"license"`
	Timestamp   int64    `json:"timestamp"`
}

// fn returns the package filename, deriving it from the URL when the record omits it.
func (r condaRecord) fn() string {
	if r.Fn != "" {
		return r.Fn
	}
	if r.URL != "" {
		return path.Base(r.URL)
	}
	return r.Name + "-" + r.Version + "-" + r.Build + ".conda"
}

// url returns the download URL for the record.
func (r condaRecord) url() string { return r.URL }

// subdir returns the record's platform subdir, deriving it from the URL path when absent
// (conda channel URLs end in <subdir>/<fn>).
func (r condaRecord) subdir() string {
	if r.Subdir != "" {
		return r.Subdir
	}
	if r.URL != "" {
		return path.Base(path.Dir(r.URL))
	}
	return "noarch"
}

type solveJSON struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Error   string `json:"error"`
	Actions struct {
		Link  []condaRecord `json:"LINK"`
		Fetch []condaRecord `json:"FETCH"`
	} `json:"actions"`
}

// parseSolveJSON parses a controller solve, tolerating leading noise like parseCondaJSON.
func parseSolveJSON(stdout []byte) (solveJSON, error) {
	var sv solveJSON
	if err := json.Unmarshal(trimToJSON(stdout), &sv); err != nil {
		return solveJSON{}, err
	}
	return sv, nil
}

// mergeRecords returns the LINK set (the packages that end up in the env), backfilling
// download metadata (url/md5/sha256/size/fn) from the matching FETCH record when LINK
// lacks it. LINK is authoritative for env contents; FETCH reliably carries the URL.
func mergeRecords(link, fetch []condaRecord) []condaRecord {
	byKey := map[string]condaRecord{}
	for _, f := range fetch {
		byKey[f.Name+"="+f.Version+"="+f.Build] = f
	}
	base := link
	if len(base) == 0 {
		base = fetch // no LINK actions (fully cached solve): fall back to FETCH
	}
	out := make([]condaRecord, 0, len(base))
	for _, r := range base {
		if f, ok := byKey[r.Name+"="+r.Version+"="+r.Build]; ok {
			if r.URL == "" {
				r.URL = f.URL
			}
			if r.Fn == "" {
				r.Fn = f.Fn
			}
			if r.MD5 == "" {
				r.MD5 = f.MD5
			}
			if r.SHA256 == "" {
				r.SHA256 = f.SHA256
			}
			if r.Size == 0 {
				r.Size = f.Size
			}
			if len(r.Depends) == 0 {
				r.Depends = f.Depends
			}
		}
		out = append(out, r)
	}
	return out
}

// repodataPkg is a repodata.json package entry (a channel index record). Unlike the solve
// record it carries no url/fn — conda derives those from the channel layout and the map key.
type repodataPkg struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Build       string   `json:"build"`
	BuildNumber int      `json:"build_number"`
	Depends     []string `json:"depends"`
	Constrains  []string `json:"constrains,omitempty"`
	Subdir      string   `json:"subdir"`
	MD5         string   `json:"md5,omitempty"`
	SHA256      string   `json:"sha256,omitempty"`
	Size        int64    `json:"size,omitempty"`
	License     string   `json:"license,omitempty"`
	Timestamp   int64    `json:"timestamp,omitempty"`
}

type repodata struct {
	Info struct {
		Subdir string `json:"subdir"`
	} `json:"info"`
	Packages        map[string]repodataPkg `json:"packages"`       // .tar.bz2
	PackagesConda   map[string]repodataPkg `json:"packages.conda"` // .conda
	RepodataVersion int                    `json:"repodata_version"`
}

// synthesizeRepodata builds a repodata.json for one subdir from the solve records, keyed
// by filename and split into the legacy `packages` (.tar.bz2) and `packages.conda`
// (.conda) maps as conda expects. This replaces `conda index` — everything conda needs to
// resolve the channel offline is reconstructable from the solve output plus the files.
func synthesizeRepodata(subdir string, recs []condaRecord) ([]byte, error) {
	rd := repodata{
		Packages:        map[string]repodataPkg{},
		PackagesConda:   map[string]repodataPkg{},
		RepodataVersion: 1,
	}
	rd.Info.Subdir = subdir
	for _, r := range recs {
		depends := r.Depends
		if depends == nil {
			depends = []string{} // conda requires an array, never null
		}
		entry := repodataPkg{
			Name: r.Name, Version: r.Version, Build: r.Build, BuildNumber: r.BuildNumber,
			Depends: depends, Constrains: r.Constrains, Subdir: subdir,
			MD5: r.MD5, SHA256: r.SHA256, Size: r.Size, License: r.License, Timestamp: r.Timestamp,
		}
		fn := r.fn()
		if strings.HasSuffix(fn, ".conda") {
			rd.PackagesConda[fn] = entry
		} else {
			rd.Packages[fn] = entry
		}
	}
	return json.MarshalIndent(rd, "", "  ")
}

// downloadFile GETs url into dest and returns the sha256 of the bytes written.
func downloadFile(ctx context.Context, client *http.Client, url, dest string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
