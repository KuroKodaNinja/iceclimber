//go:build functional

package functional

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/probe"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestCondaRelayEnvAndInstall is the air-gapped conda headline end to end: the sandbox
// ships conda but has NO channel-seeded packages, so the controller (host conda) solves
// the environment for the sandbox's platform, downloads the packages, synthesizes a
// self-contained local channel, pushes it in, and the sandbox creates a python-3.12 conda
// env and installs `six` entirely OFFLINE from that channel. It proves, together: the
// conda env_manager + the relay tier (controller solve → repodata synthesis → offline
// install) with no sandbox network access.
//
// Skipped unless BOTH the glibc box has conda (miniforge provision — rebuild the VM if it
// predates it) AND the controller has conda (the relay's solve+download engine). `six` is
// a tiny pure-python package chosen to keep the solve/download fast.
func TestCondaRelayEnvAndInstall(t *testing.T) {
	if !controllerHasConda() {
		t.Skip("conda relay needs conda on the controller (host has none); set controller_conda")
	}
	sb := requireGlibcSandbox(t)
	requireSandboxConda(t, sb)

	root := "/tmp/iceclimber-conda-" + protocol.NewID()
	cfg := writeCondaConfig(t, sb, root)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	// Relay: the controller resolves + downloads the conda-forge closure for python 3.12 +
	// six, pushes a file:// channel, and the sandbox creates the env offline from it.
	out := runIceclimber(t, "install", "conda", "six",
		"--config", cfg, "--transport", "sftp", "--python", "3.12", "--tier", "relay",
		"--conda-arg=-c", "--conda-arg=conda-forge")
	if !strings.Contains(strings.ToLower(string(out)), "six") {
		t.Errorf("expected six in the install output:\n%s", out)
	}

	// import six from the conda env interpreter — the real proof the offline env built + ran.
	condaPy := root + "/envs/conda-python-3.12/bin/python"
	got := limaShOn(t, sb.Name, condaPy+` -c 'import six; print(six.__version__)'`)
	if strings.TrimSpace(got) == "" {
		t.Errorf("import six from the conda env produced no version:\n%s", got)
	}
}

// controllerCondaBin returns the conda-compatible binary on the controller PATH ("conda"
// or the drop-in "mamba"), or "" if neither is present. It must be a real executable:
// the relay invokes it via exec, so a shell alias (e.g. `alias conda=mamba`) does not
// count — hence probing mamba directly.
func controllerCondaBin() string {
	for _, b := range []string{"conda", "mamba"} {
		if _, err := exec.LookPath(b); err == nil && exec.Command(b, "--version").Run() == nil {
			return b
		}
	}
	return ""
}

// controllerHasConda reports whether a usable conda/mamba is on the controller (the
// relay's solve+download engine). Mirrors controllerHasPip.
func controllerHasConda() bool { return controllerCondaBin() != "" }

// requireSandboxConda skips the test unless the glibc box's probe reports a conda binary
// (the miniforge provision). Boxes provisioned before miniforge was added skip with a
// rebuild hint rather than failing.
func requireSandboxConda(t *testing.T, sb sandboxConn) {
	t.Helper()
	out := runIceclimber(t, "probe", "--json", "--config", writeConfigFor(t, sb, ""))
	var fp probe.Fingerprint
	if err := json.Unmarshal(out, &fp); err != nil {
		t.Fatalf("probe --json: %v\n%s", err, out)
	}
	py, ok := fp.Runtime("python")
	if !ok || py.CondaPath == "" {
		t.Skip("no conda on the glibc box; rebuild it (`make sandbox-glibc-down && make sandbox-glibc-up`) for the miniforge provision")
	}
}

// writeCondaConfig pins python to the system source with env_manager conda (so the env is
// a conda env) and sets controller_conda to the controller's actual conda/mamba binary
// (the relay invokes it via exec, so a `conda` shell alias won't do).
func writeCondaConfig(t *testing.T, sb sandboxConn, root string) string {
	t.Helper()
	scheduleRootCleanupOn(t, sb.Name, root)
	return writeYAML(t, sshConfigYAML(sb)+fmt.Sprintf(
		"remote_root: %s\ncontroller_conda: %s\nruntimes:\n  python:\n    source: system\n    env_manager: conda\n",
		root, controllerCondaBin()))
}
