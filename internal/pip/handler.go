package pip

import (
	"context"
	"encoding/json"
	"path"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/python"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Deps are what a pip.install needs from the session: where to run, the
// sandbox fingerprint (to locate the runtime), and the mirror config.
type Deps struct {
	FS            remotefs.FS
	Runner        remote.Runner
	Root          string
	Arch          string
	Libc          string
	IndexURL      string
	ExtraIndexURL string
	TrustedHost   string
}

// Run locates the target runtime, resolves the specs, and installs them. A
// non-nil error means resolution (or locating the runtime) failed — the whole
// request fails. A nil error with per-package failures in the Outcome is the
// partial-success case (plan §4.3). Shared by the CLI and the protocol handler.
func Run(ctx context.Context, d Deps, pythonVersion string, specs []pkg.Spec) (pkg.Outcome, error) {
	bin, err := python.Locate(ctx, d.FS, d.Root, pythonVersion, d.Arch, d.Libc)
	if err != nil {
		return pkg.Outcome{}, err
	}
	m := New(Config{
		Runner:        d.Runner,
		FS:            d.FS,
		PythonBin:     bin,
		StateDir:      path.Join(d.Root, "state"),
		IndexURL:      d.IndexURL,
		ExtraIndexURL: d.ExtraIndexURL,
		TrustedHost:   d.TrustedHost,
	})
	plan, err := m.Resolve(ctx, specs)
	if err != nil {
		return pkg.Outcome{}, err
	}
	return m.Install(ctx, plan)
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
		out, err := Run(ctx, d, p.PythonVersion, specs)
		if err != nil {
			return protocol.Errf(req.ID, "resolution_failed", "%v", err)
		}
		return protocol.OK(req.ID, out)
	}
}
