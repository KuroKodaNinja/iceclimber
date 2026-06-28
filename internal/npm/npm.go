// Package npm is the npm package manager for the Node runtime — the second
// language after pip (plan §5, decision #22). Tier 0 runs npm in the sandbox
// against a reachable registry; Tier 1 (relay) downloads on the controller and
// relays the node_modules tree in. Packages install globally into the runtime's
// prefix; the result returns the NODE_PATH the agent exports to require them.
package npm

import (
	"context"
	"encoding/json"
	"path"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Config holds the npm manager's dependencies.
type Config struct {
	Runner   remote.Runner
	FS       remotefs.FS
	NodeBin  string // <node-dir>/bin/node
	NpmBin   string // <node-dir>/bin/npm (a JS file run via node)
	Prefix   string // <node-dir> — the global install prefix
	NodePath string // <node-dir>/lib/node_modules — what the agent sets as NODE_PATH
	// Tier 0:
	RegistryURL string
	// Tier 1 (relay) only:
	Arch               string
	Libc               string
	ControllerNpm      string // operator's npm on the controller (default "npm")
	ControllerRegistry string // Popo-reachable registry (default npmjs)
}

// Manager installs npm packages into one Node runtime.
type Manager struct {
	cfg Config
}

// New builds an npm manager.
func New(cfg Config) *Manager { return &Manager{cfg: cfg} }

// Install (Tier 0) installs each spec globally into the runtime prefix against the
// configured (or default) registry, one at a time so a single failure is
// attributable.
func (m *Manager) Install(ctx context.Context, specs []pkg.Spec) (pkg.Outcome, error) {
	var out pkg.Outcome
	for _, s := range specs {
		res, err := m.cfg.Runner.Run(ctx, m.installCmd(ref(s)), nil)
		switch {
		case err != nil:
			out.Failed = append(out.Failed, pkg.Failure{Name: s.Name, Version: s.Version, Error: err.Error()})
		case res.ExitCode != 0:
			out.Failed = append(out.Failed, pkg.Failure{Name: s.Name, Version: s.Version, Error: lastLines(res.Stderr, 3)})
		default:
			ver, _ := m.installedVersion(ctx, s.Name)
			if ver == "" {
				ver = s.Version
			}
			out.Installed = append(out.Installed, pkg.Installed{Name: s.Name, Version: ver, Tier: pkg.TierMirror})
		}
	}
	return out, nil
}

// npmCmd builds an npm invocation: run the npm CLI via the runtime's node, with
// the runtime's bin on PATH so npm's child node processes resolve (no global node).
func (m *Manager) npmCmd(args ...string) string {
	binDir := path.Dir(m.cfg.NodeBin)
	head := []string{
		"PATH=" + remote.ShellQuote(binDir) + ":$PATH",
		remote.ShellQuote(m.cfg.NodeBin), remote.ShellQuote(m.cfg.NpmBin),
	}
	return strings.Join(append(head, args...), " ")
}

func (m *Manager) installCmd(ref string) string {
	args := []string{
		"install", "-g", remote.ShellQuote(ref),
		"--prefix", remote.ShellQuote(m.cfg.Prefix),
		"--no-fund", "--no-audit", "--no-progress",
	}
	if m.cfg.RegistryURL != "" {
		args = append(args, "--registry", remote.ShellQuote(m.cfg.RegistryURL))
	}
	return m.npmCmd(args...)
}

// installedVersion reads the installed package's version from its package.json.
func (m *Manager) installedVersion(ctx context.Context, name string) (string, error) {
	data, err := m.cfg.FS.ReadFile(ctx, path.Join(m.cfg.NodePath, name, "package.json"))
	if err != nil {
		return "", err
	}
	var p struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return "", err
	}
	return p.Version, nil
}

// ref renders a spec as npm expects it ("name" or "name@version").
func ref(s pkg.Spec) string {
	if s.Version == "" {
		return s.Name
	}
	return s.Name + "@" + s.Version
}

func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
