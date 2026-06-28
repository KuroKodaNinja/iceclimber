package npm

import (
	"context"
	"encoding/json"
	"path"

	"github.com/KuroKodaNinja/iceclimber/internal/node"
	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Deps are what an npm.install needs from the session.
type Deps struct {
	FS                 remotefs.FS
	Runner             remote.Runner
	Root               string
	Arch               string
	Libc               string
	RegistryURL        string
	ControllerNpm      string
	ControllerRegistry string
}

// Result is the npm.install response body: the neutral install outcome plus the
// NODE_PATH the agent exports so `require()` finds the installed packages.
type Result struct {
	Installed []pkg.Installed `json:"installed"`
	Failed    []pkg.Failure   `json:"failed"`
	NodePath  string          `json:"node_path"`
}

// Run locates the Node runtime and installs the specs via the selected tier.
func Run(ctx context.Context, d Deps, nodeVersion string, specs []pkg.Spec, tier string) (Result, error) {
	nodeBin, err := node.Locate(ctx, d.FS, d.Root, nodeVersion, d.Arch, d.Libc)
	if err != nil {
		return Result{}, err
	}
	dir := path.Dir(path.Dir(nodeBin)) // <node-dir> (strip /bin/node)
	m := New(Config{
		Runner:             d.Runner,
		FS:                 d.FS,
		NodeBin:            nodeBin,
		NpmBin:             path.Join(dir, "bin", "npm"),
		Prefix:             dir,
		NodePath:           path.Join(dir, "lib", "node_modules"),
		RegistryURL:        d.RegistryURL,
		Arch:               d.Arch,
		Libc:               d.Libc,
		ControllerNpm:      d.ControllerNpm,
		ControllerRegistry: d.ControllerRegistry,
	})

	result := func(o pkg.Outcome) Result {
		return Result{Installed: o.Installed, Failed: o.Failed, NodePath: m.cfg.NodePath}
	}
	if resolveTier(tier, d.RegistryURL) == pkg.TierRelay {
		out, err := m.RelayInstall(ctx, specs)
		if err != nil {
			return Result{}, err
		}
		return result(out), nil
	}
	out, err := m.Install(ctx, specs)
	if err != nil {
		return Result{}, err
	}
	// Tier 0→1 auto-fallback (mirror pip #25): if auto-mirror installed nothing
	// (registry unreachable, or none of the packages are there), try the relay.
	if (tier == "" || tier == "auto") && len(out.Installed) == 0 && len(out.Failed) > 0 {
		if relayOut, relayErr := m.RelayInstall(ctx, specs); relayErr == nil {
			return result(relayOut), nil
		}
	}
	return result(out), nil
}

// resolveTier maps the requested tier to a concrete one. "auto" picks relay when
// no sandbox-reachable registry is configured (the air-gapped default), else
// mirror. "mirror"/"relay" force the choice.
func resolveTier(tier, registryURL string) string {
	switch tier {
	case pkg.TierRelay:
		return pkg.TierRelay
	case pkg.TierMirror:
		return pkg.TierMirror
	default: // "auto" or ""
		if registryURL == "" {
			return pkg.TierRelay
		}
		return pkg.TierMirror
	}
}

type installParams struct {
	NodeVersion string `json:"node_version"`
	Packages    []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"packages"`
}

// Handler adapts npm.Run into the npm.install protocol handler.
func Handler(d Deps) protocol.Handler {
	return func(ctx context.Context, req protocol.Request) protocol.Response {
		var p installParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return protocol.Errf(req.ID, "malformed_params", "parse params: %v", err)
		}
		if p.NodeVersion == "" {
			return protocol.Errf(req.ID, "missing_node_version", "npm.install requires params.node_version")
		}
		if len(p.Packages) == 0 {
			return protocol.Errf(req.ID, "no_packages", "npm.install requires at least one package")
		}
		specs := make([]pkg.Spec, len(p.Packages))
		for i, pp := range p.Packages {
			specs[i] = pkg.Spec{Name: pp.Name, Version: pp.Version}
		}
		out, err := Run(ctx, d, p.NodeVersion, specs, "auto")
		if err != nil {
			return protocol.Errf(req.ID, "install_failed", "%v", err)
		}
		return protocol.OK(req.ID, out)
	}
}
