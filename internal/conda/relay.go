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

	// 4. Build the env OFFLINE in the sandbox from the pushed file:// channel. If the env
	//    already exists (a prior relay/Tier-0 install), add the specs with an offline
	//    `conda install` instead of `conda create` (which refuses a populated prefix) — so
	//    relay is idempotent and additive, matching the Tier-0 path.
	m.cfg.Progress.Phase("installing (offline)")
	chanURL := "file://" + sandboxChan
	exists := m.envPythonExists(ctx, m.cfg.EnvPrefix)
	res, err := m.cfg.Runner.Run(ctx, m.offlineEnvCmd(m.cfg.EnvPrefix, chanURL, minor, specs, exists), nil)
	if err != nil {
		return pkg.Outcome{}, fmt.Errorf("run offline conda env build: %w", err)
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

// downloadChannel fetches every record's package file into localChan/<subdir>/<fn> over
// https, verifying sha256 when the solve provided one, and returns name -> sha256 of the
// downloaded file so installed packages can be reported with a content hash. (Records from
// modern conda carry sha256; the md5-only legacy case is left unverified — the payload is
// installed offline into the already-untrusted sandbox, never executed on the controller.)
func (m *Manager) downloadChannel(ctx context.Context, localChan string, recs []condaRecord) (map[string]string, error) {
	client := m.httpClient()
	shas := map[string]string{}
	for i, r := range recs {
		url, fn, sub := r.url(), r.fn(), r.subdir()
		if url == "" {
			return nil, fmt.Errorf("solve record %s-%s has no download URL", r.Name, r.Version)
		}
		if !strings.HasPrefix(url, "https://") {
			return nil, fmt.Errorf("refusing non-https package URL for %s: %s", fn, firstNonEmpty(schemeOf(url), "(none)"))
		}
		if err := safePathComponent(fn); err != nil {
			return nil, err
		}
		if err := safePathComponent(sub); err != nil {
			return nil, err
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

// offlineEnvCmd builds the sandbox-side offline env command, drawing exclusively from the
// pushed file:// channel (--offline --override-channels), so no network is used. A fresh
// env is `conda create … python=<minor> <specs>`; an existing env (idempotent/additive
// re-install) is `conda install … <specs>` (python is already present) — `conda create`
// refuses a populated prefix.
func (m *Manager) offlineEnvCmd(prefix, chanURL, minor string, specs []pkg.Spec, envExists bool) string {
	verb := "create"
	if envExists {
		verb = "install"
	}
	args := []string{
		remote.ShellQuote(m.cfg.CondaBin), verb, "-y", "--json",
		"-p", remote.ShellQuote(prefix),
		"--offline", "--override-channels",
		// The default libmamba solver cannot load a synthesized local repodata.json for a
		// file:// channel ("Could not load repodata"); the classic solver reads it fine and
		// is always built into conda. Forcing it keeps the relay working without shipping a
		// zstd-compressed repodata.json.zst (which libmamba would otherwise require).
		"--solver", "classic",
		"-c", remote.ShellQuote(chanURL),
	}
	if !envExists {
		args = append(args, remote.ShellQuote("python="+minor))
	}
	for _, s := range specs {
		args = append(args, remote.ShellQuote(condaSpec(s)))
	}
	return strings.Join(args, " ")
}

// envPythonExists reports whether the conda env's interpreter already exists (so the relay
// installs into it rather than trying to create over a populated prefix). Mirrors the
// Tier-0 reuse check in python.ensureCondaEnv.
func (m *Manager) envPythonExists(ctx context.Context, prefix string) bool {
	res, err := m.cfg.Runner.Run(ctx, "[ -x "+remote.ShellQuote(path.Join(prefix, "bin", "python"))+" ] && echo ok", nil)
	return err == nil && strings.TrimSpace(string(res.Stdout)) == "ok"
}

// controllerChannelArgs keeps only the channel-selection flags from the agent's
// allowlisted extra args for the CONTROLLER solve (-c/--channel/--override-channels). The
// sandbox-only offline flags (--offline/--use-local) are dropped: the controller must
// reach the real channel to resolve and download.
func controllerChannelArgs(extraArgs []string) []string {
	var out []string
	for i := 0; i < len(extraArgs); i++ {
		a := extraArgs[i]
		switch {
		case a == "-c" || a == "--channel": // two-token form: flag then value
			if i+1 < len(extraArgs) {
				out = append(out, a, extraArgs[i+1])
				i++
			}
		case strings.HasPrefix(a, "-c=") || strings.HasPrefix(a, "--channel="): // inline form
			out = append(out, a)
		case a == "--override-channels":
			out = append(out, a)
		}
	}
	return out
}

// httpClient returns the configured relay download client, or a default with a generous
// timeout for large packages.
func (m *Manager) httpClient() *http.Client {
	if m.cfg.HTTPClient != nil {
		return m.cfg.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

// schemeOf returns the URL scheme ("https", "http", …) or "" when there is none.
func schemeOf(u string) string {
	if i := strings.Index(u, "://"); i > 0 {
		return u[:i]
	}
	return ""
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
// path.Base is applied unconditionally: the record comes from a solve against a possibly
// agent-chosen channel, so a hostile repodata could otherwise smuggle a path (e.g.
// "../../.bashrc") that path.Join would resolve into a parent-dir escape when the file is
// written on the controller or sandbox.
func (r condaRecord) fn() string {
	if r.Fn != "" {
		return path.Base(r.Fn)
	}
	if r.URL != "" {
		return path.Base(r.URL)
	}
	return r.Name + "-" + r.Version + "-" + r.Build + ".conda"
}

// url returns the download URL for the record.
func (r condaRecord) url() string { return r.URL }

// subdir returns the record's platform subdir, deriving it from the URL path when absent
// (conda channel URLs end in <subdir>/<fn>). path.Base guards against a hostile subdir
// value (see fn); the caller additionally validates it against the safe set.
func (r condaRecord) subdir() string {
	if r.Subdir != "" {
		return path.Base(r.Subdir)
	}
	if r.URL != "" {
		return path.Base(path.Dir(r.URL))
	}
	return "noarch"
}

// safePathComponent rejects a channel path component that could escape the channel dir. A
// solve resolved against an agent-selected channel is attacker-influenced JSON; without
// this a crafted fn/subdir ("..", "a/b") would steer a write out of the transient channel
// tree (path.Join cleans ".." into a parent escape).
func safePathComponent(s string) error {
	if s == "" || s == "." || s == ".." || strings.ContainsAny(s, `/\`) {
		return fmt.Errorf("unsafe channel path component %q", s)
	}
	return nil
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
