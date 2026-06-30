//go:build functional

package functional

import (
	"encoding/json"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/probe"
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

func hasEnvManager(rt probe.RuntimeInfo, want string) bool {
	for _, m := range rt.EnvManagers {
		if m == want {
			return true
		}
	}
	return false
}
