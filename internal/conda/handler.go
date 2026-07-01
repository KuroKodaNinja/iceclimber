package conda

import (
	"context"
	"encoding/json"
	"fmt"
	"path"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/python"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Deps are what a conda.install needs from the session: where to run, the sandbox conda
// + fingerprint, and (for the relay tier) the controller's conda.
type Deps struct {
	FS              remotefs.FS
	Runner          remote.Runner
	Root            string
	Arch            string
	Libc            string
	CondaBin        string // sandbox conda binary (from the probe)
	ControllerConda string // operator's conda on the controller (relay tier; default "conda")
	Progress        progress.Func
}

// extraArgAllow is the per-request conda flag allowlist — chiefly channel selection
// (what `conda install -c conda-forge` needs) plus the no-network offline flags. Like
// pip's, it is deliberately narrow: no build/solver-behavior flags, and bare positionals
// are rejected by ValidateExtraArgs so it can never become a shell.
var extraArgAllow = map[string]pkg.FlagSpec{
	"-c":                  {TakesValue: true},
	"--channel":           {TakesValue: true},
	"--override-channels": {},
	"--offline":           {},
	"--use-local":         {},
}

// Run creates/reuses a conda env for the requested python and installs the specs into
// it. extraArgs are validated allowlisted conda flags (channels, offline). Tier "relay"
// is air-gapped (relay.go); "auto"/"mirror"/"" run Tier-0 against the sandbox's channel.
// Shared by the CLI and the handler.
func Run(ctx context.Context, d Deps, pythonVersion string, specs []pkg.Spec, tier string, extraArgs []string) (pkg.Outcome, error) {
	if err := pkg.ValidateExtraArgs(extraArgs, extraArgAllow); err != nil {
		return pkg.Outcome{}, err
	}
	if d.CondaBin == "" {
		return pkg.Outcome{}, fmt.Errorf("no conda on the sandbox (probe reported none)")
	}

	// The relay tier builds the env OFFLINE from a controller-pushed channel, so it
	// bypasses python.EnsureEnv (which would try an online `conda create`) and targets the
	// env prefix directly. Tier-0 goes through EnsureEnv against the sandbox's channel.
	if resolveTier(tier, extraArgs) == pkg.TierRelay {
		m := New(Config{
			Runner: d.Runner, FS: d.FS, CondaBin: d.CondaBin,
			EnvPrefix: python.CondaEnvPrefix(d.Root, pythonVersion),
			Root:      d.Root, Progress: d.Progress, ExtraArgs: extraArgs,
			Arch: d.Arch, Libc: d.Libc, ControllerConda: d.ControllerConda,
		})
		return m.RelayInstall(ctx, specs, python.MinorOf(pythonVersion))
	}

	d.Progress.Phase("resolving")
	// A conda env is always a conda env regardless of the operator's python source, so
	// force EnvManager=conda; EnsureEnv creates/reuses <root>/envs/conda-python-<minor>.
	bin, err := python.EnsureEnv(ctx, d.FS, d.Runner, d.Root, pythonVersion, d.Arch, d.Libc,
		python.EnvSpec{Mode: "system", EnvManager: "conda", CondaBin: d.CondaBin})
	if err != nil {
		return pkg.Outcome{}, err
	}
	m := New(Config{
		Runner: d.Runner, FS: d.FS, CondaBin: d.CondaBin,
		EnvPrefix: path.Dir(path.Dir(bin)), // <root>/envs/conda-python-<minor>
		Root:      d.Root, Progress: d.Progress, ExtraArgs: extraArgs,
		Arch: d.Arch, Libc: d.Libc, ControllerConda: d.ControllerConda,
	})
	return m.Install(ctx, specs)
}

// resolveTier maps the requested tier to a concrete one. "relay"/"mirror" force it; "auto"
// (or "") picks relay when the agent asked for offline (there is no cheap probe of channel
// reachability at request time, so an explicit --offline is the auto signal), else mirror.
func resolveTier(tier string, extraArgs []string) string {
	switch tier {
	case pkg.TierRelay:
		return pkg.TierRelay
	case pkg.TierMirror:
		return pkg.TierMirror
	default: // "auto" or ""
		if pkg.ExtraArgsHaveFlag(extraArgs, "--offline") {
			return pkg.TierRelay
		}
		return pkg.TierMirror
	}
}

type installParams struct {
	PythonVersion string `json:"python_version"`
	Packages      []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"packages"`
	// ExtraArgs are allowlisted conda flags passed straight through (e.g. -c conda-forge).
	ExtraArgs []string `json:"extra_args,omitempty"`
	// File, when set, is a sandbox path to an environment.yml — build the whole env from
	// it (manifest-driven) instead of an explicit package list.
	File string `json:"file,omitempty"`
}

// Handler adapts conda.Run into the conda.install protocol handler.
func Handler(d Deps) protocol.Handler {
	return func(ctx context.Context, req protocol.Request) protocol.Response {
		var p installParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return protocol.Errf(req.ID, "malformed_params", "parse params: %v", err)
		}
		// Manifest-driven: build the whole env from a sandbox environment.yml.
		if p.File != "" {
			out, err := RunManifest(ctx, d, p.File, "auto")
			if err != nil {
				return protocol.Errf(req.ID, "install_failed", "%v", err)
			}
			return protocol.OK(req.ID, out)
		}
		if p.PythonVersion == "" {
			return protocol.Errf(req.ID, "missing_python_version", "conda.install requires params.python_version (or params.file)")
		}
		if len(p.Packages) == 0 {
			return protocol.Errf(req.ID, "no_packages", "conda.install requires at least one package")
		}
		specs := make([]pkg.Spec, len(p.Packages))
		for i, pp := range p.Packages {
			specs[i] = pkg.Spec{Name: pp.Name, Version: pp.Version}
		}
		out, err := Run(ctx, d, p.PythonVersion, specs, "auto", p.ExtraArgs)
		if err != nil {
			return protocol.Errf(req.ID, "install_failed", "%v", err)
		}
		return protocol.OK(req.ID, out)
	}
}
