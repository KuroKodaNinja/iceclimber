// Package pip is the Tier-0 pip package manager: it resolves a request against
// an index, then retrieves/installs each resolved package — running pip
// in-sandbox over the exec channel (plan §4.3, §5). Resolution is co-resolved
// (native fidelity); retrieval is per-package (attributable failures).
package pip

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Config holds the pip manager's dependencies. Tier 0 runs pip in the sandbox
// via Runner; Tier 1 (relay) additionally runs the controller's python locally.
type Config struct {
	Runner        remote.Runner
	FS            remotefs.FS
	PythonBin     string // absolute path to the sandbox runtime's bin/python3
	Root          string // sandbox install root (for blobs/wheels)
	StateDir      string // sandbox dir for the report file
	IndexURL      string // Tier 0 mirror (sandbox-reachable)
	ExtraIndexURL string
	TrustedHost   string
	// Tier 1 (relay) only:
	Arch               string // sandbox fingerprint, for the wheel platform tags
	Libc               string
	ControllerPython   string // operator's python on the controller (default python3)
	ControllerIndexURL string // Popo-reachable index to download from (default PyPI)
	Progress           progress.Func
	// ExtraArgs are validated, allowlisted pip flags the agent passed through (e.g.
	// --index-url for PyTorch's wheel index, --pre). They are appended to the
	// index-facing pip commands (resolve, Tier-0 install, relay download).
	ExtraArgs []string
}

// Manager installs pip packages into one runtime.
type Manager struct {
	cfg Config
}

// New builds a pip manager.
func New(cfg Config) *Manager { return &Manager{cfg: cfg} }

// Resolve co-resolves specs against the index (native behavior; unversioned
// specs resolve by pip's default) and returns the exact pinned plan with hashes.
// A failed dependency graph is a request-level error — correct native behavior.
func (m *Manager) Resolve(ctx context.Context, specs []pkg.Spec) (pkg.Plan, error) {
	if !m.hasIndex() {
		return pkg.Plan{}, fmt.Errorf("no pip index available (set pip.index_url or pass --index-url via extra_args)")
	}
	if len(specs) == 0 {
		return pkg.Plan{}, fmt.Errorf("no packages requested")
	}
	if err := m.cfg.FS.Mkdir(ctx, m.cfg.StateDir); err != nil {
		return pkg.Plan{}, fmt.Errorf("ensure state dir: %w", err)
	}
	reportPath := path.Join(m.cfg.StateDir, "pip-report.json")

	res, err := m.cfg.Runner.Run(ctx, m.resolveCmd(specs, reportPath), nil)
	if err != nil {
		return pkg.Plan{}, fmt.Errorf("run pip resolve: %w", err)
	}
	if res.ExitCode != 0 {
		return pkg.Plan{}, fmt.Errorf("pip could not resolve the request: %s", lastLines(res.Stderr, 4))
	}
	data, err := m.cfg.FS.ReadFile(ctx, reportPath)
	if err != nil {
		return pkg.Plan{}, fmt.Errorf("read pip report: %w", err)
	}
	return parseReport(data)
}

// Install retrieves and installs each resolved package independently, so a single
// pull failure is attributable. --no-deps because resolution already happened.
func (m *Manager) Install(ctx context.Context, plan pkg.Plan) (pkg.Outcome, error) {
	var out pkg.Outcome
	for i, p := range plan.Packages {
		m.cfg.Progress.Emit(progress.Event{Phase: "installing " + p.Name, Cur: int64(i + 1), Total: int64(len(plan.Packages)), Unit: progress.Items})
		res, err := m.cfg.Runner.Run(ctx, m.installCmd(p), nil)
		switch {
		case err != nil:
			out.Failed = append(out.Failed, pkg.Failure{Name: p.Name, Version: p.Version, Error: err.Error()})
		case res.ExitCode != 0:
			out.Failed = append(out.Failed, pkg.Failure{Name: p.Name, Version: p.Version, Error: lastLines(res.Stderr, 3)})
		default:
			out.Installed = append(out.Installed, pkg.Installed{
				Name: p.Name, Version: p.Version, Tier: pkg.TierMirror, SHA256: p.SHA256,
			})
		}
	}
	return out, nil
}

func (m *Manager) resolveCmd(specs []pkg.Spec, reportPath string) string {
	args := []string{
		remote.ShellQuote(m.cfg.PythonBin), "-m", "pip", "install",
		"--dry-run", "--no-input", "--disable-pip-version-check",
		"--report", remote.ShellQuote(reportPath),
	}
	args = append(args, m.indexArgs()...)
	args = append(args, m.quotedExtraArgs()...)
	for _, s := range specs {
		args = append(args, remote.ShellQuote(specString(s)))
	}
	return strings.Join(args, " ")
}

func (m *Manager) installCmd(p pkg.Resolved) string {
	args := []string{
		remote.ShellQuote(m.cfg.PythonBin), "-m", "pip", "install",
		"--no-deps", "--no-input", "--disable-pip-version-check",
	}
	args = append(args, m.indexArgs()...)
	args = append(args, m.quotedExtraArgs()...)
	args = append(args, remote.ShellQuote(p.Name+"=="+p.Version))
	return strings.Join(args, " ")
}

func (m *Manager) indexArgs() []string {
	var a []string
	if m.cfg.IndexURL != "" {
		a = append(a, "--index-url", remote.ShellQuote(m.cfg.IndexURL))
	}
	if m.cfg.ExtraIndexURL != "" {
		a = append(a, "--extra-index-url", remote.ShellQuote(m.cfg.ExtraIndexURL))
	}
	if m.cfg.TrustedHost != "" {
		a = append(a, "--trusted-host", remote.ShellQuote(m.cfg.TrustedHost))
	}
	return a
}

// quotedExtraArgs renders the agent's allowlisted extra args, each shell-quoted.
// Appended after indexArgs so an agent --index-url takes precedence over config.
func (m *Manager) quotedExtraArgs() []string {
	out := make([]string, len(m.cfg.ExtraArgs))
	for i, a := range m.cfg.ExtraArgs {
		out[i] = remote.ShellQuote(a)
	}
	return out
}

// hasIndex reports whether an index is available for a Tier-0 resolve — from
// operator config or an agent-supplied --index-url in extra_args.
func (m *Manager) hasIndex() bool {
	return m.cfg.IndexURL != "" || pkg.ExtraArgsHaveFlag(m.cfg.ExtraArgs, "--index-url", "-i")
}

// specString renders a spec as pip expects it (bare name if unversioned).
func specString(s pkg.Spec) string {
	if s.Version == "" {
		return s.Name
	}
	return s.Name + "==" + s.Version
}

// pipReport is the subset of `pip install --report` JSON we consume.
type pipReport struct {
	Install []struct {
		Metadata struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"metadata"`
		DownloadInfo struct {
			URL         string `json:"url"`
			ArchiveInfo struct {
				Hashes map[string]string `json:"hashes"`
			} `json:"archive_info"`
		} `json:"download_info"`
	} `json:"install"`
}

func parseReport(data []byte) (pkg.Plan, error) {
	var rep pipReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return pkg.Plan{}, fmt.Errorf("parse pip report: %w", err)
	}
	plan := pkg.Plan{Packages: make([]pkg.Resolved, 0, len(rep.Install))}
	for _, it := range rep.Install {
		plan.Packages = append(plan.Packages, pkg.Resolved{
			Name:    it.Metadata.Name,
			Version: it.Metadata.Version,
			URL:     it.DownloadInfo.URL,
			SHA256:  it.DownloadInfo.ArchiveInfo.Hashes["sha256"], // "" for sdists
		})
	}
	return plan, nil
}

// lastLines returns the trailing n non-empty-trimmed lines of b — enough error
// context without the full pip log.
func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
