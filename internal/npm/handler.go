package npm

import (
	"context"
	"encoding/json"
	"path"

	"github.com/KuroKodaNinja/iceclimber/internal/node"
	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
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
	Progress           progress.Func
	// RuntimeMode "system" uses the sandbox's own node/npm (SystemNodePath) and installs into
	// an iceclimber-owned prefix under Root (never the system global) — the node analogue of a
	// system-python venv. Empty/"managed" uses the iceclimber-installed node.
	RuntimeMode    string
	SystemNodePath string
}

// Result is the npm.install response body: the neutral install outcome plus the
// NODE_PATH the agent exports so `require()` finds the installed packages.
type Result struct {
	Installed []pkg.Installed `json:"installed"`
	Failed    []pkg.Failure   `json:"failed"`
	NodePath  string          `json:"node_path"`
}

// nodeSetup resolves the node/npm binaries + global install prefix for the chosen runtime
// mode. Managed: the iceclimber-installed node under <root>, prefix = its own dir. System:
// the detected node/npm, prefix = an iceclimber-owned dir under <root> — so installs stay
// writable and off the system global (the node analogue of a system-python venv).
func nodeSetup(ctx context.Context, d Deps, nodeVersion string) (nodeBin, npmBin, prefix, nodePath string, err error) {
	if d.RuntimeMode == "system" {
		nodeBin = d.SystemNodePath
		if nodeBin == "" {
			nodeBin = "node" // fall back to PATH
		}
		prefix = path.Join(d.Root, "runtimes", "node-system")
		if err = d.FS.Mkdir(ctx, prefix); err != nil {
			return "", "", "", "", err
		}
		return nodeBin, path.Join(path.Dir(nodeBin), "npm"), prefix, path.Join(prefix, "lib", "node_modules"), nil
	}
	nodeBin, err = node.Locate(ctx, d.FS, d.Root, nodeVersion, d.Arch, d.Libc)
	if err != nil {
		return "", "", "", "", err
	}
	dir := path.Dir(path.Dir(nodeBin)) // <node-dir> (strip /bin/node)
	return nodeBin, path.Join(dir, "bin", "npm"), dir, path.Join(dir, "lib", "node_modules"), nil
}

// Run locates the Node runtime and installs the specs via the selected tier.
func Run(ctx context.Context, d Deps, nodeVersion string, specs []pkg.Spec, tier string) (Result, error) {
	d.Progress.Phase("resolving")
	nodeBin, npmBin, prefix, nodePath, err := nodeSetup(ctx, d, nodeVersion)
	if err != nil {
		return Result{}, err
	}
	m := New(Config{
		Runner:             d.Runner,
		FS:                 d.FS,
		NodeBin:            nodeBin,
		NpmBin:             npmBin,
		Prefix:             prefix,
		NodePath:           nodePath,
		RegistryURL:        d.RegistryURL,
		Arch:               d.Arch,
		Libc:               d.Libc,
		ControllerNpm:      d.ControllerNpm,
		ControllerRegistry: d.ControllerRegistry,
	})

	result := func(o pkg.Outcome) Result {
		return Result{Installed: o.Installed, Failed: o.Failed, NodePath: m.cfg.NodePath}
	}
	d.Progress.Phase("installing")
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

// RunProject installs a full npm project's dependencies from its package.json in the
// sandbox (manifest-driven). Relay: the controller's npm resolves + installs and the tree
// is relayed into <projectDir>/node_modules; mirror: the sandbox's own npm installs in
// place. Either way the project runs with ordinary local ./node_modules resolution.
func RunProject(ctx context.Context, d Deps, nodeVersion, projectDir, tier string) (Result, error) {
	d.Progress.Phase("resolving")
	nodeBin, npmBin, prefix, nodePath, err := nodeSetup(ctx, d, nodeVersion)
	if err != nil {
		return Result{}, err
	}
	m := New(Config{
		Runner: d.Runner, FS: d.FS, NodeBin: nodeBin, NpmBin: npmBin,
		Prefix: prefix, NodePath: nodePath, RegistryURL: d.RegistryURL,
		Arch: d.Arch, Libc: d.Libc, ControllerNpm: d.ControllerNpm, ControllerRegistry: d.ControllerRegistry,
	})
	d.Progress.Phase("installing")
	var out pkg.Outcome
	if resolveTier(tier, d.RegistryURL) == pkg.TierRelay {
		out, err = m.RelayInstallProject(ctx, projectDir)
	} else {
		out, err = m.InstallProject(ctx, projectDir)
	}
	if err != nil {
		return Result{}, err
	}
	// A project resolves ./node_modules locally — no global NODE_PATH needed.
	return Result{Installed: out.Installed, Failed: out.Failed, NodePath: path.Join(projectDir, "node_modules")}, nil
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
	// Project, when set, is a sandbox directory holding a package.json — manifest-driven
	// install (npm install/ci) instead of an explicit package list.
	Project string `json:"project,omitempty"`
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
		// Manifest-driven install from a sandbox project's package.json.
		if p.Project != "" {
			out, err := RunProject(ctx, d, p.NodeVersion, p.Project, "auto")
			if err != nil {
				return protocol.Errf(req.ID, "install_failed", "%v", err)
			}
			return protocol.OK(req.ID, out)
		}
		if len(p.Packages) == 0 {
			return protocol.Errf(req.ID, "no_packages", "npm.install requires at least one package (or params.project)")
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
