package npm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

// lockfiles npm recognizes, in the order that makes `npm ci` deterministic.
var lockfiles = []string{"package-lock.json", "npm-shrinkwrap.json"}

// RelayInstallProject is the manifest-driven Tier-1 path: the sandbox holds a real npm
// project (a package.json, optionally a lockfile), and the controller's npm resolves +
// installs its dependencies, then Popo relays the resulting node_modules into
// <projectDir>/node_modules. The project runs with ordinary local resolution — no
// NODE_PATH, because node finds ./node_modules from the app's own directory. Pure-JS only
// (a native build gets the controller's platform — Node "Tier 2", future work).
func (m *Manager) RelayInstallProject(ctx context.Context, projectDir string) (pkg.Outcome, error) {
	cnpm := m.cfg.ControllerNpm
	if cnpm == "" {
		cnpm = "npm"
	}
	if out, err := exec.CommandContext(ctx, cnpm, "--version").CombinedOutput(); err != nil {
		return pkg.Outcome{}, fmt.Errorf("Tier 1 relay needs npm on the controller (set controller_npm): %v: %s", err, lastLines(out, 3))
	}

	// 1. Read the manifest (and any lockfile) from the sandbox project.
	manifest, err := m.cfg.FS.ReadFile(ctx, path.Join(projectDir, "package.json"))
	if err != nil {
		return pkg.Outcome{}, fmt.Errorf("read %s/package.json (create the project first): %w", projectDir, err)
	}
	deps, err := manifestDeps(manifest)
	if err != nil {
		return pkg.Outcome{}, err
	}

	stage, err := os.MkdirTemp("", "iceclimber-npmproj-")
	if err != nil {
		return pkg.Outcome{}, err
	}
	defer os.RemoveAll(stage)
	if err := os.WriteFile(filepath.Join(stage, "package.json"), manifest, 0o644); err != nil {
		return pkg.Outcome{}, err
	}

	// A lockfile makes the install reproducible: honor it with `npm ci` when present.
	verb := "install"
	for _, lf := range lockfiles {
		if data, rerr := m.cfg.FS.ReadFile(ctx, path.Join(projectDir, lf)); rerr == nil {
			if err := os.WriteFile(filepath.Join(stage, lf), data, 0o644); err != nil {
				return pkg.Outcome{}, err
			}
			verb = "ci"
			break
		}
	}

	// 2. Controller install into the staged project (produces stage/node_modules).
	args := []string{verb, "--no-fund", "--no-audit", "--no-progress"}
	if m.cfg.ControllerRegistry != "" {
		args = append(args, "--registry", m.cfg.ControllerRegistry)
	}
	cmd := exec.CommandContext(ctx, cnpm, args...)
	cmd.Dir = stage
	if out, err := cmd.CombinedOutput(); err != nil {
		return pkg.Outcome{}, fmt.Errorf("controller npm %s failed (a dep may need a native build — Node Tier 2 is future work): %s", verb, lastLines(out, 6))
	}

	stageModules := filepath.Join(stage, "node_modules")
	if _, err := os.Stat(stageModules); err != nil {
		return pkg.Outcome{}, fmt.Errorf("controller npm %s produced no node_modules", verb)
	}

	// 3. Relay node_modules into the sandbox project. Idempotent Symlink handles a
	//    re-install over an existing tree.
	if err := m.pushDir(ctx, stageModules, path.Join(projectDir, "node_modules")); err != nil {
		return pkg.Outcome{}, fmt.Errorf("relay node_modules: %w", err)
	}

	// 4. Attribute per top-level dependency, versioned from the staged install.
	var out pkg.Outcome
	for _, name := range deps {
		v := readPkgVersion(filepath.Join(stageModules, filepath.FromSlash(name), "package.json"))
		if v == "" {
			out.Failed = append(out.Failed, pkg.Failure{Name: name, Error: "not present in the resolved project"})
			continue
		}
		out.Installed = append(out.Installed, pkg.Installed{Name: name, Version: v, Tier: pkg.TierRelay})
	}
	return out, nil
}

// InstallProject is the Tier-0 manifest path: run the sandbox's own npm inside the
// project directory against a reachable registry. Needs sandbox network; used when a
// mirror tier is explicitly selected.
func (m *Manager) InstallProject(ctx context.Context, projectDir string) (pkg.Outcome, error) {
	manifest, err := m.cfg.FS.ReadFile(ctx, path.Join(projectDir, "package.json"))
	if err != nil {
		return pkg.Outcome{}, fmt.Errorf("read %s/package.json (create the project first): %w", projectDir, err)
	}
	deps, err := manifestDeps(manifest)
	if err != nil {
		return pkg.Outcome{}, err
	}
	args := []string{"install", "--no-fund", "--no-audit", "--no-progress"}
	if m.cfg.RegistryURL != "" {
		args = append(args, "--registry", m.cfg.RegistryURL)
	}
	// Run npm in the project dir (cwd) so it installs into ./node_modules.
	cmd := "cd " + remote.ShellQuote(projectDir) + " && " + m.npmCmd(args...)
	res, err := m.cfg.Runner.Run(ctx, cmd, nil)
	if err != nil {
		return pkg.Outcome{}, fmt.Errorf("run npm install in %s: %w", projectDir, err)
	}
	if res.ExitCode != 0 {
		return pkg.Outcome{}, fmt.Errorf("npm install in %s failed: %s", projectDir, lastLines(res.Stderr, 4))
	}
	var out pkg.Outcome
	for _, name := range deps {
		data, rerr := m.cfg.FS.ReadFile(ctx, path.Join(projectDir, "node_modules", name, "package.json"))
		v := ""
		if rerr == nil {
			var p struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(data, &p) == nil {
				v = p.Version
			}
		}
		if v == "" {
			out.Failed = append(out.Failed, pkg.Failure{Name: name, Error: "not present after install"})
			continue
		}
		out.Installed = append(out.Installed, pkg.Installed{Name: name, Version: v, Tier: pkg.TierMirror})
	}
	return out, nil
}

// manifestDeps returns the top-level dependency names from a package.json
// (dependencies + devDependencies), for attribution. Order is stable (sorted).
func manifestDeps(manifest []byte) ([]string, error) {
	var p struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(manifest, &p); err != nil {
		return nil, fmt.Errorf("parse package.json: %w", err)
	}
	seen := map[string]bool{}
	var names []string
	for _, set := range []map[string]string{p.Dependencies, p.DevDependencies} {
		for name := range set {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names, nil
}
