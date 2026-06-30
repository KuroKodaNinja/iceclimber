//go:build functional

package functional

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/probe"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestSystemPythonVenvInstall is the brownfield headline: with python=system, an
// install creates an iceclimber-owned venv from the box's system python (never
// touching system site-packages) and installs into it. Uses a C-extension wheel
// (markupsafe) relayed from the controller and tag-matched to the system python, then
// imports it from the venv — proving the venv + tag-matched relay path end to end.
func TestSystemPythonVenvInstall(t *testing.T) {
	if !controllerHasPip() {
		t.Skip("tag-matched relay needs python3 + pip on the controller")
	}
	sb := requireGlibcSandbox(t)

	minor := systemPythonMinor(t, sb)
	root := "/tmp/iceclimber-sysvenv-" + protocol.NewID()
	cfg := writeSystemPythonConfig(t, sb, root)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	// No `install python` first: system mode creates the venv on demand.
	out := runIceclimber(t, "install", "pip", "markupsafe==2.1.5",
		"--config", cfg, "--transport", "sftp", "--python", minor, "--tier", "relay")
	if !strings.Contains(string(out), "(relay)") {
		t.Errorf("expected a relay-tier install:\n%s", out)
	}

	// Installs land in an iceclimber-owned venv (pyvenv.cfg proves it's an isolated
	// venv under the root, not the system site-packages), and the EXACT pinned version
	// imports from the venv interpreter — so it came from our install, not whatever the
	// distro's system python happens to ship.
	venvDir := fmt.Sprintf("%s/envs/python-%s", root, minor)
	if got := limaShOn(t, sb.Name, `[ -f `+venvDir+`/pyvenv.cfg ] && echo venv || echo missing`); !strings.Contains(got, "venv") {
		t.Errorf("expected an isolated venv at %s (pyvenv.cfg), got %q", venvDir, got)
	}
	if got := limaShOn(t, sb.Name, venvDir+`/bin/python -c 'import markupsafe; print(markupsafe.__version__)'`); !strings.Contains(got, "2.1.5") {
		t.Errorf("import markupsafe from venv = %q, want exactly 2.1.5", got)
	}
}

// TestSystemPythonVersionMismatchFails proves the headline safety guarantee end to
// end: in system mode, requesting a python the box doesn't have fails clearly and
// never mutates the system toolchain — iceclimber uses what's there or refuses.
func TestSystemPythonVersionMismatchFails(t *testing.T) {
	sb := requireGlibcSandbox(t)
	minor := systemPythonMinor(t, sb) // e.g. "3.12"
	if minor == "3.11" {
		t.Skip("box already has 3.11; need a version it lacks for the mismatch")
	}
	root := "/tmp/iceclimber-mismatch-" + protocol.NewID()
	cfg := writeSystemPythonConfig(t, sb, root)
	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	var stderr bytes.Buffer
	cmd := exec.Command(iceclimberBin, "install", "pip", "six",
		"--config", cfg, "--transport", "sftp", "--python", "3.11") // box has 3.12, not 3.11
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("requesting a python version the box lacks should fail in system mode")
	}
	if !strings.Contains(stderr.String(), minor) {
		t.Errorf("error should name the system version %s; got: %s", minor, stderr.String())
	}
	// The venv for the wrong version must NOT have been created.
	chk := limaShOn(t, sb.Name, "[ -d "+root+"/envs/python-3.11 ] && echo exists || echo absent")
	if !strings.Contains(chk, "absent") {
		t.Errorf("a venv for the unavailable version should not exist: %q", chk)
	}
}

// systemPythonMinor probes the glibc box for its system python's "<maj>.<min>".
func systemPythonMinor(t *testing.T, sb sandboxConn) string {
	t.Helper()
	out := runIceclimber(t, "probe", "--json", "--config", writeConfigFor(t, sb, ""))
	var fp probe.Fingerprint
	if err := json.Unmarshal(out, &fp); err != nil {
		t.Fatalf("probe --json: %v\n%s", err, out)
	}
	py, ok := fp.Runtime("python")
	if !ok || py.Version == "" {
		t.Skipf("no system python on the glibc box; runtimes=%+v", fp.Runtimes)
	}
	parts := strings.Split(py.Version, ".")
	if len(parts) < 2 {
		t.Fatalf("unexpected python version %q", py.Version)
	}
	return parts[0] + "." + parts[1]
}

// writeSystemPythonConfig writes a config pinning python to the system source
// (shared ssh block + remote_root, plus the runtimes override).
func writeSystemPythonConfig(t *testing.T, sb sandboxConn, root string) string {
	t.Helper()
	scheduleRootCleanupOn(t, sb.Name, root)
	return writeYAML(t, sshConfigYAML(sb)+fmt.Sprintf("remote_root: %s\nruntimes:\n  python:\n    source: system\n", root))
}
