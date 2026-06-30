package pip

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

// Deps are what a pip.install needs from the session: where to run, the
// sandbox fingerprint (to locate the runtime + pick wheel tags), and config.
type Deps struct {
	FS                 remotefs.FS
	Runner             remote.Runner
	Root               string
	Arch               string
	Libc               string
	IndexURL           string
	ExtraIndexURL      string
	TrustedHost        string
	ControllerPython   string
	ControllerIndexURL string
	Progress           progress.Func // optional operator-facing progress (nil = silent)

	// RuntimeMode selects where the interpreter comes from: "" / "managed" (locate an
	// iceclimber runtime) or "system" (install into a venv built from a system python).
	RuntimeMode string
	SystemPath  string // system interpreter (system mode); "" → python3 on PATH
	EnvManager  string // system mode: "" / "venv"
}

// Run locates the target runtime and installs the specs via the selected tier
// (plan §5). A non-nil error means resolution/download (or locating the runtime)
// failed — the whole request fails. A nil error with per-package failures in the
// Outcome is the partial-success case (plan §4.3). Shared by CLI and handler.
func Run(ctx context.Context, d Deps, pythonVersion string, specs []pkg.Spec, tier string) (pkg.Outcome, error) {
	d.Progress.Phase("resolving")
	// Resolve the interpreter for the chosen runtime source: a managed iceclimber
	// runtime, or a venv built from a system python (created on demand). Both yield an
	// absolute interpreter the rest of the install path uses unchanged.
	bin, err := python.EnsureEnv(ctx, d.FS, d.Runner, d.Root, pythonVersion, d.Arch, d.Libc,
		python.EnvSpec{Mode: d.RuntimeMode, SystemPath: d.SystemPath, EnvManager: d.EnvManager})
	if err != nil {
		return pkg.Outcome{}, err
	}
	m := New(Config{
		Runner:             d.Runner,
		FS:                 d.FS,
		PythonBin:          bin,
		Root:               d.Root,
		StateDir:           path.Join(d.Root, "state"),
		IndexURL:           d.IndexURL,
		ExtraIndexURL:      d.ExtraIndexURL,
		TrustedHost:        d.TrustedHost,
		Arch:               d.Arch,
		Libc:               d.Libc,
		ControllerPython:   d.ControllerPython,
		ControllerIndexURL: d.ControllerIndexURL,
		Progress:           d.Progress,
	})
	if resolveTier(tier, d.IndexURL) == pkg.TierRelay {
		return m.RelayInstall(ctx, specs, pythonVersion)
	}
	plan, err := m.Resolve(ctx, specs)
	if err != nil {
		// Tier 0→1 auto-fallback (decision #25): when the tier was auto-selected as
		// mirror and the mirror can't resolve a spec (e.g. it's absent there), try
		// the relay before giving up. An explicit `--tier mirror` is respected.
		if tier == "" || tier == "auto" {
			out, relayErr := m.RelayInstall(ctx, specs, pythonVersion)
			if relayErr == nil {
				return out, nil
			}
			return pkg.Outcome{}, fmt.Errorf("mirror resolve failed (%v); relay fallback also failed: %w", err, relayErr)
		}
		return pkg.Outcome{}, err
	}
	return m.Install(ctx, plan)
}

// resolveTier maps the requested tier to a concrete one. "auto" picks relay when
// no mirror is configured, else mirror. "mirror"/"relay" force the choice.
func resolveTier(tier, indexURL string) string {
	switch tier {
	case pkg.TierRelay:
		return pkg.TierRelay
	case pkg.TierMirror:
		return pkg.TierMirror
	default: // "auto" or ""
		if indexURL == "" {
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
}

// Handler adapts pip.Run into the pip.install protocol handler.
func Handler(d Deps) protocol.Handler {
	return func(ctx context.Context, req protocol.Request) protocol.Response {
		var p installParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return protocol.Errf(req.ID, "malformed_params", "parse params: %v", err)
		}
		if p.PythonVersion == "" {
			return protocol.Errf(req.ID, "missing_python_version", "pip.install requires params.python_version")
		}
		if len(p.Packages) == 0 {
			return protocol.Errf(req.ID, "no_packages", "pip.install requires at least one package")
		}
		specs := make([]pkg.Spec, len(p.Packages))
		for i, pp := range p.Packages {
			specs[i] = pkg.Spec{Name: pp.Name, Version: pp.Version}
		}
		out, err := Run(ctx, d, p.PythonVersion, specs, "auto")
		if err != nil {
			return protocol.Errf(req.ID, "resolution_failed", "%v", err)
		}
		return protocol.OK(req.ID, out)
	}
}
