// Package conda is the conda package manager, a sibling of internal/pip. Unlike pip
// (co-resolve then per-package install), conda solves an environment holistically: one
// `conda install -y --json -p <env> [-c <channel>] <specs>` resolves + installs in a
// single step. Tier-0 (this file) runs conda in the sandbox against a reachable channel;
// the relay tier (relay.go) is air-gapped. Results use the shared internal/pkg types.
package conda

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Config holds the conda manager's dependencies. Tier-0 runs conda in the sandbox via
// Runner against EnvPrefix (a conda env created by python.EnsureEnv); the relay tier
// additionally uses the controller's conda (ControllerConda) — see relay.go.
type Config struct {
	Runner    remote.Runner
	FS        remotefs.FS
	CondaBin  string // sandbox conda binary
	EnvPrefix string // the conda env to install into (<root>/envs/conda-python-<minor>)
	Root      string // sandbox install root (for relay blobs)
	Progress  progress.Func
	// ExtraArgs are validated, allowlisted conda flags the agent passed through — chiefly
	// channels (-c/--channel) and the offline flags. Appended to the install command.
	ExtraArgs []string
	// Relay-tier only (relay.go):
	Arch            string // sandbox subdir selection
	Libc            string
	ControllerConda string // operator's conda on the controller (default "conda")
}

// Manager installs conda packages into one conda env.
type Manager struct {
	cfg Config
}

// New builds a conda manager.
func New(cfg Config) *Manager { return &Manager{cfg: cfg} }

// Install solves + installs the specs into the env in one holistic conda command,
// parsing --json. A conda solve failure is reported per requested spec in Failed (a
// request-level nil error with a populated Outcome) unless the command itself couldn't
// run.
func (m *Manager) Install(ctx context.Context, specs []pkg.Spec) (pkg.Outcome, error) {
	if len(specs) == 0 {
		return pkg.Outcome{}, fmt.Errorf("no packages requested")
	}
	m.cfg.Progress.Phase("installing")
	res, err := m.cfg.Runner.Run(ctx, m.installCmd(specs), nil)
	if err != nil {
		return pkg.Outcome{}, fmt.Errorf("run conda install: %w", err)
	}
	cr, perr := parseCondaJSON(res.Stdout)
	if perr != nil {
		if res.ExitCode != 0 {
			return pkg.Outcome{}, fmt.Errorf("conda install failed: %s", lastLines(res.Stderr, 4))
		}
		return pkg.Outcome{}, fmt.Errorf("parse conda --json output: %w", perr)
	}
	if res.ExitCode != 0 || !cr.Success {
		msg := firstNonEmpty(cr.Message, cr.Error, lastLines(res.Stderr, 4), "conda install failed")
		var out pkg.Outcome
		for _, s := range specs {
			out.Failed = append(out.Failed, pkg.Failure{Name: s.Name, Version: s.Version, Error: msg})
		}
		return out, nil
	}
	// Success: report the requested specs, taking the resolved version from the LINK
	// actions where present (a spec already satisfied may not re-link).
	linked := map[string]string{}
	for _, l := range cr.Actions.Link {
		linked[l.Name] = l.Version
	}
	var out pkg.Outcome
	for _, s := range specs {
		v := s.Version
		if lv, ok := linked[s.Name]; ok {
			v = lv
		}
		out.Installed = append(out.Installed, pkg.Installed{Name: s.Name, Version: v, Tier: m.tier()})
	}
	return out, nil
}

// tier reports which tier tag to stamp on installed packages (relay when the offline
// flags are in play — set by RelayInstall — else mirror/direct).
func (m *Manager) tier() string {
	if pkg.ExtraArgsHaveFlag(m.cfg.ExtraArgs, "--offline") {
		return pkg.TierRelay
	}
	return pkg.TierMirror
}

func (m *Manager) installCmd(specs []pkg.Spec) string {
	args := []string{
		remote.ShellQuote(m.cfg.CondaBin), "install", "-y", "--json",
		"-p", remote.ShellQuote(m.cfg.EnvPrefix),
	}
	args = append(args, m.quotedExtraArgs()...)
	for _, s := range specs {
		args = append(args, remote.ShellQuote(condaSpec(s)))
	}
	return strings.Join(args, " ")
}

// quotedExtraArgs renders the agent's allowlisted conda flags (channels, offline), each
// shell-quoted.
func (m *Manager) quotedExtraArgs() []string {
	out := make([]string, len(m.cfg.ExtraArgs))
	for i, a := range m.cfg.ExtraArgs {
		out[i] = remote.ShellQuote(a)
	}
	return out
}

// condaSpec renders a spec as conda expects it: "name" or "name=version" (single '=',
// conda's match-spec form — unlike pip's "==").
func condaSpec(s pkg.Spec) string {
	if s.Version == "" {
		return s.Name
	}
	return s.Name + "=" + s.Version
}

// condaJSON is the subset of `conda install --json` we consume.
type condaJSON struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Error   string `json:"error"`
	Actions struct {
		Link []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"LINK"`
	} `json:"actions"`
}

// parseCondaJSON extracts the JSON doc conda --json prints. conda sometimes emits
// leading non-JSON noise on stderr but the stdout doc is clean; we take the first
// balanced {...} object.
func parseCondaJSON(stdout []byte) (condaJSON, error) {
	s := strings.TrimSpace(string(stdout))
	if i := strings.IndexByte(s, '{'); i > 0 {
		s = s[i:] // tolerate a stray prefix line
	}
	var cr condaJSON
	if err := json.Unmarshal([]byte(s), &cr); err != nil {
		return condaJSON{}, err
	}
	return cr, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// lastLines returns the trailing n non-empty-trimmed lines of b.
func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
