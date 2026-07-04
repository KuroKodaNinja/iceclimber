//go:build functional

package functional

import (
	"fmt"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// pinnedFull is the exact CPython patch the runtime_pins block below pins 3.11 to. It is an
// OLDER patch than what the live PBS resolver would pick for 3.11 (currently 3.11.15+), so an
// install landing on this version is proof the operator pin bypassed the live resolver (§17).
const pinnedFull = "3.11.13"

// runtimePinsYAML pins python 3.11 to a fixed PBS asset per aarch64 platform (both live boxes
// are aarch64: the musl Alpine box and the glibc Ubuntu box). The URLs + sha256s are real,
// checksum-verified 20251007 PBS releases — the air-gapped-mirror path a compliance operator
// would encode against their own mirror.
const runtimePinsYAML = `runtime_pins:
  python:
    "3.11":
      full: 3.11.13
      platforms:
        linux/aarch64/musl:
          url: https://github.com/astral-sh/python-build-standalone/releases/download/20251007/cpython-3.11.13+20251007-aarch64-unknown-linux-musl-install_only.tar.gz
          sha256: 5169d922a93adf7c53d126a2e5f95e1c915574546523baed484699b82951b362
        linux/aarch64/glibc:
          url: https://github.com/astral-sh/python-build-standalone/releases/download/20251007/cpython-3.11.13+20251007-aarch64-unknown-linux-gnu-install_only.tar.gz
          sha256: 9e399423248aeae4f4355ea3f5f6d75585bcdd4bee7254c0d29e3db91eb98580
`

// TestRuntimePinPython proves an operator runtime pin drives the install: `install python 3.11`
// against a config whose runtime_pins fix 3.11→3.11.13 installs exactly 3.11.13 (not the live
// latest), the checksum-pinned tarball verifies + runs on the box, and re-install is idempotent.
// Exec transport (the bulk tar push) on the musl box; the glibc platform key is present too so
// the same block works on either aarch64 box.
func TestRuntimePinPython(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-pypin-" + protocol.NewID()
	cfg := writeYAML(t, sshConfigYAML(sb)+fmt.Sprintf("remote_root: %s\n", root)+runtimePinsYAML)
	scheduleRootCleanup(t, root)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "exec")

	out := string(runIceclimber(t, "install", "python", "3.11", "--config", cfg, "--transport", "exec"))
	if !strings.Contains(out, "python "+pinnedFull) {
		t.Errorf("install did not use the pinned version %s:\n%s", pinnedFull, out)
	}

	// The pinned patch — not the live-latest 3.11 — is what landed on the box.
	py := strings.TrimSpace(limaSh(t, "ls "+root+"/runtimes/python/"+pinnedFull+"-*/bin/python3 2>/dev/null | head -1"))
	if py == "" {
		t.Fatalf("no python %s under runtimes/python — the pin did not drive the install dir", pinnedFull)
	}
	if v := limaSh(t, remoteQuote(py)+" --version 2>&1"); !strings.Contains(v, pinnedFull) {
		t.Errorf("pinned python --version = %q, want %s", strings.TrimSpace(v), pinnedFull)
	}

	// Idempotent re-install (same pinned target).
	if out2 := string(runIceclimber(t, "install", "python", "3.11", "--config", cfg, "--transport", "exec")); !strings.Contains(out2, "already installed") {
		t.Errorf("expected idempotent re-install of the pinned runtime, got:\n%s", out2)
	}
}
