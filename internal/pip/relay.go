package pip

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

// RelayInstall is Tier 1 (plan §5): Popo downloads the sandbox-platform wheels on
// its own network, relays them in, and installs them offline. The sandbox needs
// nothing but the already-installed interpreter.
func (m *Manager) RelayInstall(ctx context.Context, specs []pkg.Spec, minor string) (pkg.Outcome, error) {
	cpy := m.cfg.ControllerPython
	if cpy == "" {
		cpy = "python3"
	}
	idx := m.cfg.ControllerIndexURL
	if idx == "" {
		idx = "https://pypi.org/simple"
	}
	if out, err := exec.CommandContext(ctx, cpy, "-m", "pip", "--version").CombinedOutput(); err != nil {
		return pkg.Outcome{}, fmt.Errorf("Tier 1 relay needs python+pip on the controller (set controller_python): %v: %s", err, strings.TrimSpace(string(out)))
	}

	localDir, err := os.MkdirTemp("", "iceclimber-wheels-")
	if err != nil {
		return pkg.Outcome{}, err
	}
	defer os.RemoveAll(localDir)

	// 1. Controller download — resolve + fetch sandbox-platform wheels.
	dl := exec.CommandContext(ctx, cpy, downloadArgs(specs, minor, m.cfg.Arch, m.cfg.Libc, idx, localDir)...)
	if out, err := dl.CombinedOutput(); err != nil {
		return pkg.Outcome{}, fmt.Errorf("controller wheel download failed (a package may lack a %s wheel — Tier 2 build is future work): %s", m.cfg.Libc, lastLines(out, 5))
	}
	wheels, err := filepath.Glob(filepath.Join(localDir, "*.whl"))
	if err != nil {
		return pkg.Outcome{}, err
	}
	if len(wheels) == 0 {
		return pkg.Outcome{}, fmt.Errorf("controller downloaded no wheels")
	}

	// 2. Relay wheels into a per-request sandbox dir.
	sandboxDir := path.Join(m.cfg.Root, "blobs", "wheels-"+protocol.NewID())
	if err := m.cfg.FS.Mkdir(ctx, sandboxDir); err != nil {
		return pkg.Outcome{}, fmt.Errorf("create sandbox wheel dir: %w", err)
	}
	hashes := map[string]string{}
	for _, w := range wheels {
		data, err := os.ReadFile(w)
		if err != nil {
			return pkg.Outcome{}, err
		}
		sum := sha256.Sum256(data)
		name, version := wheelNameVersion(w)
		hashes[normName(name)+"@"+version] = hex.EncodeToString(sum[:])
		if err := m.cfg.FS.WriteFile(ctx, path.Join(sandboxDir, filepath.Base(w)), data); err != nil {
			return pkg.Outcome{}, fmt.Errorf("relay wheel %s: %w", filepath.Base(w), err)
		}
	}

	// 3. Offline install in the sandbox, co-resolved from the relayed wheels.
	if err := m.cfg.FS.Mkdir(ctx, m.cfg.StateDir); err != nil {
		return pkg.Outcome{}, err
	}
	reportPath := path.Join(m.cfg.StateDir, "pip-report.json")
	res, err := m.cfg.Runner.Run(ctx, m.offlineInstallCmd(specs, sandboxDir, reportPath), nil)
	if err != nil {
		return pkg.Outcome{}, fmt.Errorf("run offline install: %w", err)
	}
	if res.ExitCode != 0 {
		return pkg.Outcome{}, fmt.Errorf("offline install failed: %s", lastLines(res.Stderr, 4))
	}
	data, err := m.cfg.FS.ReadFile(ctx, reportPath)
	if err != nil {
		return pkg.Outcome{}, fmt.Errorf("read offline-install report: %w", err)
	}
	plan, err := parseReport(data)
	if err != nil {
		return pkg.Outcome{}, err
	}

	var out pkg.Outcome
	for _, p := range plan.Packages {
		out.Installed = append(out.Installed, pkg.Installed{
			Name:    p.Name,
			Version: p.Version,
			Tier:    pkg.TierRelay,
			SHA256:  hashes[normName(p.Name)+"@"+p.Version], // hashed from the relayed wheel
		})
	}
	return out, nil
}

// downloadArgs builds the controller-side cross-platform `pip download`. These
// args go to exec.Command directly (no shell), so no quoting.
func downloadArgs(specs []pkg.Spec, minor, arch, libc, indexURL, dest string) []string {
	args := []string{
		"-m", "pip", "download",
		"--only-binary=:all:", // cross-platform: no building sdists for a foreign target
		"--dest", dest,
		"--python-version", minor,
		"--implementation", "cp",
		"--abi", "cp" + strings.ReplaceAll(minor, ".", ""),
		"--index-url", indexURL,
	}
	for _, t := range platformTags(arch, libc) {
		args = append(args, "--platform", t)
	}
	for _, s := range specs {
		args = append(args, specString(s))
	}
	return args
}

// platformTags returns the wheel platform tags the sandbox accepts, newest
// first. pip downloads the best wheel matching any; pure-python (py3-none-any)
// wheels match regardless.
func platformTags(arch, libc string) []string {
	switch libc {
	case "musl":
		return []string{"musllinux_1_2_" + arch, "musllinux_1_1_" + arch}
	case "glibc":
		return []string{"manylinux_2_28_" + arch, "manylinux_2_17_" + arch, "manylinux2014_" + arch}
	default:
		return []string{"linux_" + arch}
	}
}

// offlineInstallCmd installs the relayed wheels in the sandbox with no index.
func (m *Manager) offlineInstallCmd(specs []pkg.Spec, wheelDir, reportPath string) string {
	args := []string{
		remote.ShellQuote(m.cfg.PythonBin), "-m", "pip", "install",
		"--no-index", "--no-input", "--disable-pip-version-check",
		"--find-links", remote.ShellQuote(wheelDir),
		"--report", remote.ShellQuote(reportPath),
	}
	for _, s := range specs {
		args = append(args, remote.ShellQuote(specString(s)))
	}
	return strings.Join(args, " ")
}

// wheelNameVersion extracts the distribution name and version from a wheel
// filename: <name>-<version>-<pytag>-<abitag>-<platform>.whl.
func wheelNameVersion(p string) (name, version string) {
	base := strings.TrimSuffix(filepath.Base(p), ".whl")
	parts := strings.SplitN(base, "-", 3)
	if len(parts) < 2 {
		return base, ""
	}
	return parts[0], parts[1]
}

// normName applies PEP 503 normalization so the report's "charset-normalizer"
// matches a "charset_normalizer-..." wheel.
func normName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if r == '-' || r == '_' || r == '.' {
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
			continue
		}
		b.WriteRune(r)
		prevDash = false
	}
	return b.String()
}
