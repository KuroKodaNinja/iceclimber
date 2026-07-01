package conda

import (
	"context"
	"fmt"
	"path"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/python"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

// environmentYML is the subset of a conda environment.yml the bridge consumes.
type environmentYML struct {
	Name         string      `yaml:"name"`
	Channels     []string    `yaml:"channels"`
	Dependencies []yaml.Node `yaml:"dependencies"`
}

// RunManifest is the manifest-driven conda path: the sandbox holds a real conda project
// (an environment.yml), and the bridge builds the whole environment from it. Relay: the
// controller solves the manifest's packages for the sandbox platform, downloads them,
// pushes a local channel, and the sandbox creates the env offline; mirror: the sandbox's
// own conda creates the env from a reachable channel. The env is created at
// <root>/envs/<name> (the manifest's name), or conda-python-<minor> if unnamed.
func RunManifest(ctx context.Context, d Deps, manifestPath, tier string) (pkg.Outcome, error) {
	if d.CondaBin == "" {
		return pkg.Outcome{}, fmt.Errorf("no conda on the sandbox (probe reported none)")
	}
	data, err := d.FS.ReadFile(ctx, manifestPath)
	if err != nil {
		return pkg.Outcome{}, fmt.Errorf("read %s (create the environment.yml first): %w", manifestPath, err)
	}
	name, channelArgs, pythonVersion, specs, err := parseEnvironment(data)
	if err != nil {
		return pkg.Outcome{}, err
	}
	if len(specs) == 0 {
		return pkg.Outcome{}, fmt.Errorf("environment.yml lists no packages beyond python")
	}

	// Defense-in-depth: the channel args are derived from the (agent-authored) manifest,
	// so validate them against the same allowlist the explicit path uses.
	if err := pkg.ValidateExtraArgs(channelArgs, extraArgAllow); err != nil {
		return pkg.Outcome{}, err
	}
	prefix := python.CondaEnvPrefix(d.Root, pythonVersion)
	if name != "" {
		prefix = path.Join(d.Root, "envs", name)
	}
	m := New(Config{
		Runner: d.Runner, FS: d.FS, CondaBin: d.CondaBin,
		EnvPrefix: prefix, Root: d.Root, Progress: d.Progress, ExtraArgs: channelArgs,
		Arch: d.Arch, Libc: d.Libc, ControllerConda: d.ControllerConda,
	})
	if resolveTier(tier, channelArgs) == pkg.TierRelay {
		return m.RelayInstall(ctx, specs, python.MinorOf(pythonVersion))
	}
	// Mirror: one holistic `conda create` builds the named env + all packages from a
	// reachable channel.
	return m.CreateEnv(ctx, pythonVersion, specs)
}

// CreateEnv (Tier-0 manifest path) creates the env prefix and installs all specs in one
// holistic `conda create -y --json -p <prefix> [-c …] python=<minor> <specs>` against a
// reachable channel.
func (m *Manager) CreateEnv(ctx context.Context, pythonVersion string, specs []pkg.Spec) (pkg.Outcome, error) {
	minor := python.MinorOf(pythonVersion)
	if minor == "" {
		return pkg.Outcome{}, fmt.Errorf("environment.yml must pin a python version (e.g. `- python=3.12`)")
	}
	m.cfg.Progress.Phase("creating env")
	args := []string{
		remote.ShellQuote(m.cfg.CondaBin), "create", "-y", "--json",
		"-p", remote.ShellQuote(m.cfg.EnvPrefix),
	}
	args = append(args, m.quotedExtraArgs()...)
	args = append(args, remote.ShellQuote("python="+minor))
	for _, s := range specs {
		args = append(args, remote.ShellQuote(condaSpec(s)))
	}
	res, err := m.cfg.Runner.Run(ctx, strings.Join(args, " "), nil)
	if err != nil {
		return pkg.Outcome{}, fmt.Errorf("run conda create: %w", err)
	}
	return m.resultOutcome(res, specs, pkg.TierMirror)
}

// parseEnvironment extracts the pieces the relay/create need from an environment.yml: the
// env name, channel args (`-c <chan>`), the pinned python version, and the remaining conda
// specs. A `pip:` subsection is rejected (conda-only for now).
func parseEnvironment(data []byte) (name string, channelArgs []string, pythonVersion string, specs []pkg.Spec, err error) {
	var env environmentYML
	if err = yaml.Unmarshal(data, &env); err != nil {
		return "", nil, "", nil, fmt.Errorf("parse environment.yml: %w", err)
	}
	name = strings.TrimSpace(env.Name)
	for _, ch := range env.Channels {
		if ch = strings.TrimSpace(ch); ch != "" {
			channelArgs = append(channelArgs, "-c", ch)
		}
	}
	for _, node := range env.Dependencies {
		switch node.Kind {
		case yaml.ScalarNode:
			spec := parseCondaDep(node.Value)
			if spec.Name == "python" {
				pythonVersion = spec.Version
				continue
			}
			if spec.Name != "" {
				specs = append(specs, spec)
			}
		case yaml.MappingNode:
			// The only mapping conda allows in dependencies is `{pip: [...]}`.
			return "", nil, "", nil, fmt.Errorf("environment.yml `pip:` subsection is not supported yet; list conda packages only")
		}
	}
	if pythonVersion == "" {
		return "", nil, "", nil, fmt.Errorf("environment.yml must pin a python version (e.g. `- python=3.12`)")
	}
	return name, channelArgs, pythonVersion, specs, nil
}

// parseCondaDep splits a conda dependency string into a Spec. The name is the leading run
// of package-name characters; the remainder is the version/constraint. It handles the
// plain forms ("pytorch", "python=3.12", "pandas==2.2", "numpy 1.26") and operator
// constraints ("numpy>=1.20", "numpy >=1.20", "pkg!=1.5") — the leading "="/"==" of an
// exact pin is stripped, while comparison operators (< > ! ~) are preserved for condaSpec.
func parseCondaDep(s string) pkg.Spec {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	})
	if i <= 0 {
		return pkg.Spec{Name: s}
	}
	name := s[:i]
	ver := strings.TrimSpace(s[i:])
	switch {
	case strings.HasPrefix(ver, "=="):
		ver = strings.TrimSpace(ver[2:])
	case strings.HasPrefix(ver, "="):
		ver = strings.TrimSpace(ver[1:])
	}
	return pkg.Spec{Name: name, Version: ver}
}
