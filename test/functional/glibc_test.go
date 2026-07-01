//go:build functional

package functional

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/probe"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestGlibcProbe verifies the glibc sandbox fixture: probe must report glibc (so
// manylinux/PyTorch wheels apply) and discover the system toolchain the template
// pre-installs (python3 with a usable venv, node, java) — the brownfield raw
// material later tests build on. Skips cleanly when the glibc box isn't up.
func TestGlibcProbe(t *testing.T) {
	sb := requireGlibcSandbox(t)
	cfg := writeConfigFor(t, sb, "")

	out := runIceclimber(t, "probe", "--json", "--config", cfg)
	var fp probe.Fingerprint
	if err := json.Unmarshal(out, &fp); err != nil {
		t.Fatalf("probe --json: %v\n%s", err, out)
	}

	if fp.Libc.Family != "glibc" || !fp.Libc.HighConfidence {
		t.Errorf("libc = %+v, want glibc with high confidence", fp.Libc)
	}
	py, ok := fp.Runtime("python")
	if !ok {
		t.Fatalf("no system python discovered on the glibc box; runtimes=%+v", fp.Runtimes)
	}
	if !hasEnvManager(py, "venv") {
		t.Errorf("system python should report a usable venv (python3-venv installed); managers=%v", py.EnvManagers)
	}
	if _, ok := fp.Runtime("node"); !ok {
		t.Error("no system node discovered on the glibc box")
	}
	if _, ok := fp.Runtime("java"); !ok {
		t.Error("no system java discovered on the glibc box")
	}
}

// TestInstallRuntimeSourceFlag: `install --runtime-source` resolves the choice, persists it
// controller-side, and the install honors it — using the system python (fast, no managed
// download) proves the flag reached the resolver. Runs under a private HOME so runtimes.json
// is isolated. Runtime source is now a post-bootstrap, install-time concern (not bootstrap's).
func TestInstallRuntimeSourceFlag(t *testing.T) {
	sb := requireGlibcSandbox(t)
	root := "/tmp/iceclimber-rt-" + protocol.NewID()
	cfg := writeConfigFor(t, sb, root)

	home := t.TempDir()
	var out bytes.Buffer
	cmd := exec.Command(iceclimberBin, "install", "python", "3.12",
		"--runtime-source", "python=system,node=managed", "--config", cfg, "--transport", "sftp")
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("install: %v\n%s", err, out.String())
	}

	data, err := os.ReadFile(filepath.Join(home, ".iceclimber", sb.Name, "runtimes.json"))
	if err != nil {
		t.Fatalf("runtimes.json not persisted: %v", err)
	}
	var got map[string]struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse runtimes.json: %v\n%s", err, data)
	}
	if got["python"].Mode != "system" || got["node"].Mode != "managed" {
		t.Errorf("persisted sources = %+v, want python=system node=managed", got)
	}
}

func hasEnvManager(rt probe.RuntimeInfo, want string) bool {
	for _, m := range rt.EnvManagers {
		if m == want {
			return true
		}
	}
	return false
}
