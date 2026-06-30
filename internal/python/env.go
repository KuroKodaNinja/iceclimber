package python

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// EnvSpec selects how to obtain the python interpreter packages install into.
type EnvSpec struct {
	// Mode is "" / "managed" (use an iceclimber-installed runtime) or "system" (use
	// a pre-existing system python via an iceclimber-owned env under the root).
	Mode string
	// SystemPath pins the system interpreter (system mode); empty uses "python3" on PATH.
	SystemPath string
	// EnvManager selects the isolation tool for system mode: "" / "venv" (default).
	// "conda" is handled by a separate manager (added later).
	EnvManager string
}

// system reports whether the spec wants a pre-existing system runtime.
func (s EnvSpec) system() bool { return s.Mode == "system" }

// EnsureEnv resolves the python interpreter to install into:
//
//   - managed: Locate the iceclimber-installed runtime (errors if absent — the
//     caller installs it first via python.install). Unchanged behavior.
//   - system : create or reuse an iceclimber-owned venv at <root>/envs/python-<minor>
//     built from the system python, and return the venv's interpreter. This keeps
//     installs off the (often PEP-668-locked, unwritable) system site-packages and
//     under $ICECLIMBER_HOME. The system python's minor version must match the
//     requested one — we use what's on the box and fail clearly otherwise, never
//     changing the system toolchain.
//
// Everything in system mode is sandbox-side shell over the Runner, so it works under
// either transport.
func EnsureEnv(ctx context.Context, fs remotefs.FS, runner remote.Runner, root, version, arch, libc string, spec EnvSpec) (string, error) {
	if !spec.system() {
		return Locate(ctx, fs, root, version, arch, libc)
	}
	if spec.EnvManager != "" && spec.EnvManager != "venv" {
		return "", fmt.Errorf("python env_manager %q not supported yet (use venv)", spec.EnvManager)
	}

	sysPy := spec.SystemPath
	if sysPy == "" {
		sysPy = "python3"
	}
	sysMinor, err := systemMinor(ctx, runner, sysPy)
	if err != nil {
		return "", err
	}
	if req := minorOf(version); req != "" && req != sysMinor {
		return "", fmt.Errorf("system python is %s but %s was requested; this box's toolchain is fixed — request python_version %s (or switch this runtime to managed)", sysMinor, version, sysMinor)
	}

	venv := path.Join(root, "envs", "python-"+sysMinor)
	bin := path.Join(venv, "bin", "python")
	if existsExecutable(ctx, runner, bin) {
		return bin, nil // reuse an already-created venv (idempotent)
	}
	res, err := runner.Run(ctx, remote.ShellQuote(sysPy)+" -m venv "+remote.ShellQuote(venv), nil)
	if err != nil {
		return "", fmt.Errorf("create venv from %s: %w", sysPy, err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("create venv from %s (is python3-venv installed?): %s", sysPy, strings.TrimSpace(string(res.Stderr)))
	}
	if v, err := runner.Run(ctx, remote.ShellQuote(bin)+" --version", nil); err != nil || v.ExitCode != 0 {
		return "", fmt.Errorf("venv interpreter not runnable at %s", bin)
	}
	return bin, nil
}

// systemMinor returns the "<maj>.<min>" of the given system python.
func systemMinor(ctx context.Context, runner remote.Runner, sysPy string) (string, error) {
	res, err := runner.Run(ctx, remote.ShellQuote(sysPy)+` -c 'import sys;print("%d.%d"%sys.version_info[:2])'`, nil)
	if err != nil {
		return "", fmt.Errorf("probe system python %s: %w", sysPy, err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("system python %s not usable: %s", sysPy, strings.TrimSpace(string(res.Stderr)))
	}
	m := strings.TrimSpace(string(res.Stdout))
	if m == "" {
		return "", fmt.Errorf("could not determine system python version (%s)", sysPy)
	}
	return m, nil
}

func existsExecutable(ctx context.Context, runner remote.Runner, p string) bool {
	res, err := runner.Run(ctx, "[ -x "+remote.ShellQuote(p)+" ] && echo ok", nil)
	return err == nil && strings.TrimSpace(string(res.Stdout)) == "ok"
}

// minorOf reduces a version to its "<maj>.<min>" prefix: "3.12.3" -> "3.12",
// "3.12" -> "3.12", "" -> "".
func minorOf(v string) string {
	parts := strings.Split(strings.TrimSpace(v), ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return strings.TrimSpace(v)
}
