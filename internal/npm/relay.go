package npm

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
)

// RelayInstall is Tier 1 (plan §5): the controller's npm resolves + installs the
// packages into a staging prefix on its own network, then Popo relays the
// resulting node_modules tree (and any CLI bins) into the runtime. The sandbox
// runs no npm. Pure-JS only — a package needing a native build gets the
// controller's platform and is the Node "Tier 2" (future work).
func (m *Manager) RelayInstall(ctx context.Context, specs []pkg.Spec) (pkg.Outcome, error) {
	cnpm := m.cfg.ControllerNpm
	if cnpm == "" {
		cnpm = "npm"
	}
	if out, err := exec.CommandContext(ctx, cnpm, "--version").CombinedOutput(); err != nil {
		return pkg.Outcome{}, fmt.Errorf("Tier 1 relay needs npm on the controller (set controller_npm): %v: %s", err, strings.TrimSpace(string(out)))
	}

	stage, err := os.MkdirTemp("", "iceclimber-npm-")
	if err != nil {
		return pkg.Outcome{}, err
	}
	defer os.RemoveAll(stage)

	// 1. Controller install into the staging prefix.
	args := []string{"install", "-g", "--prefix", stage, "--no-fund", "--no-audit", "--no-progress"}
	if m.cfg.ControllerRegistry != "" {
		args = append(args, "--registry", m.cfg.ControllerRegistry)
	}
	for _, s := range specs {
		args = append(args, ref(s))
	}
	if out, err := exec.CommandContext(ctx, cnpm, args...).CombinedOutput(); err != nil {
		return pkg.Outcome{}, fmt.Errorf("controller npm install failed (a package may need a native build — Node Tier 2 is future work): %s", lastLines(out, 5))
	}

	stageModules := filepath.Join(stage, "lib", "node_modules")
	stageBin := filepath.Join(stage, "bin")

	// 2. Relay the node_modules tree (merging with the runtime's, which holds npm)
	//    and any CLI bins into the runtime's bin dir.
	if err := m.pushDir(ctx, stageModules, m.cfg.NodePath); err != nil {
		return pkg.Outcome{}, fmt.Errorf("relay node_modules: %w", err)
	}
	if _, err := os.Stat(stageBin); err == nil {
		if err := m.pushDir(ctx, stageBin, path.Join(m.cfg.Prefix, "bin")); err != nil {
			return pkg.Outcome{}, fmt.Errorf("relay bins: %w", err)
		}
	}

	// 3. Attribute per requested package from the staged install.
	var out pkg.Outcome
	for _, s := range specs {
		v := readPkgVersion(filepath.Join(stageModules, filepath.FromSlash(s.Name), "package.json"))
		if v == "" {
			out.Failed = append(out.Failed, pkg.Failure{Name: s.Name, Version: s.Version, Error: "not present in the staged install"})
			continue
		}
		out.Installed = append(out.Installed, pkg.Installed{Name: s.Name, Version: v, Tier: pkg.TierRelay})
	}
	return out, nil
}

// pushDir mirrors a local directory tree into the sandbox over the FS, preserving
// directories, regular files (with their mode), and symlinks (npm's bin links and
// internal .bin entries).
func (m *Manager) pushDir(ctx context.Context, localRoot, remoteRoot string) error {
	if err := m.cfg.FS.Mkdir(ctx, remoteRoot); err != nil {
		return err
	}
	return filepath.WalkDir(localRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(localRoot, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dst := path.Join(remoteRoot, filepath.ToSlash(rel))
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return m.cfg.FS.Mkdir(ctx, dst)
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return m.cfg.FS.Symlink(ctx, target, dst)
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if err := m.cfg.FS.WriteFile(ctx, dst, data); err != nil {
				return err
			}
			return m.cfg.FS.Chmod(ctx, dst, info.Mode().Perm())
		default:
			return nil // skip devices/pipes/etc.
		}
	})
}

func readPkgVersion(pjPath string) string {
	data, err := os.ReadFile(pjPath)
	if err != nil {
		return ""
	}
	var p struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &p) != nil {
		return ""
	}
	return p.Version
}
