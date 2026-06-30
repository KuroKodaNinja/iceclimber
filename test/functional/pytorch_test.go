//go:build functional

package functional

import (
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestPyTorchSystemVenv is the brownfield headline end to end: PyTorch — a large
// manylinux C-extension package with NO musllinux build (so it can only work on the
// glibc box) — installed into a system-Python venv via the agent's extra_args (its
// dedicated CPU wheel index), then imported from the venv. This exercises, together:
// system runtime → venv → tag-matched relay → per-request --index-url passthrough.
//
// Heavy (torch is a ~100MB wheel + deps); skipped unless the glibc box + a controller
// pip are present. Pinned to a torch version with cp/aarch64 + cp/x86_64 CPU wheels.
func TestPyTorchSystemVenv(t *testing.T) {
	if !controllerHasPip() {
		t.Skip("PyTorch relay needs python3 + pip on the controller")
	}
	sb := requireGlibcSandbox(t)
	minor := systemPythonMinor(t, sb)
	root := "/tmp/iceclimber-torch-" + protocol.NewID()
	cfg := writeSystemPythonConfig(t, sb, root)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	// torch from PyTorch's CPU index, its deps from PyPI — the real CPU-install recipe,
	// carried entirely through allowlisted extra_args. Relay (tier 1): the controller
	// downloads the sandbox-platform wheels and pushes them into the venv.
	out := runIceclimber(t, "install", "pip", "torch==2.4.1",
		"--config", cfg, "--transport", "sftp", "--python", minor, "--tier", "relay",
		"--pip-arg=--index-url", "--pip-arg=https://download.pytorch.org/whl/cpu",
		"--pip-arg=--extra-index-url", "--pip-arg=https://pypi.org/simple")
	if !strings.Contains(strings.ToLower(string(out)), "torch") {
		t.Errorf("expected torch in the install output:\n%s", out)
	}

	// import torch from the venv interpreter — the real proof it landed and runs.
	venvPy := root + "/envs/python-" + minor + "/bin/python"
	got := limaShOn(t, sb.Name, venvPy+` -c 'import torch; print(torch.__version__)'`)
	if !strings.Contains(got, "2.4.1") {
		t.Errorf("import torch from venv = %q, want 2.4.1", got)
	}
}
