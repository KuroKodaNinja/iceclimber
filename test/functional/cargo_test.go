//go:build functional

package functional

import (
	"fmt"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestCargoBuildRelay validates the cargo.build package-manager seam on BOTH boxes via the
// air-gapped relay tier: the controller's cargo vendors a crate dependency, Popo pushes the
// vendor tree + a source-replacement .cargo/config.toml into the sandbox, and the sandbox
// compiles OFFLINE against it. The project is a LIBRARY crate on purpose — an rlib build has no
// final link step, so it needs no C toolchain (neither the musl Alpine nor the minimal glibc box
// ships cc), yet still exercises the whole seam: crate resolution → vendor → relay push → offline
// compile of the dependency + the lib. Building a *binary* would additionally need a linker the
// sandboxes don't have; that's a toolchain-provisioning concern orthogonal to the pkg seam.
func TestCargoBuildRelay(t *testing.T) {
	musl := requireSandbox(t)
	t.Run("musl", func(t *testing.T) { cargoBuildRelayOn(t, musl) })
	t.Run("glibc", func(t *testing.T) { cargoBuildRelayOn(t, requireGlibcSandbox(t)) })
}

func cargoBuildRelayOn(t *testing.T, sb sandboxConn) {
	root := "/tmp/iceclimber-cargo-" + protocol.NewID()
	// egress_mode: relay → cargo.build takes the vendored offline path (no proxy/serve needed).
	cfg := writeYAML(t, sshConfigYAML(sb)+fmt.Sprintf("remote_root: %s\negress_mode: relay\n", root))
	scheduleRootCleanupOn(t, sb.Name, root)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "exec")
	runIceclimber(t, "install", "rust", "--config", cfg, "--transport", "exec")

	// A minimal library crate with one small, pure-Rust, build-script-free dependency (itoa) —
	// enough to prove real crate resolution + offline compile without any native/link step.
	proj := root + "/proj"
	limaShOn(t, sb.Name, "mkdir -p "+proj+"/src && "+
		"printf '%s' '[package]\nname = \"probe\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\nitoa = \"1\"\n\n[lib]\npath = \"src/lib.rs\"\n' > "+proj+"/Cargo.toml && "+
		"printf '%s' 'pub fn n() -> String { let mut b = itoa::Buffer::new(); b.format(42u32).to_string() }\n' > "+proj+"/src/lib.rs")

	out := string(runIceclimber(t, "cargo", "build", "--project", proj, "--config", cfg, "--transport", "exec"))
	if !strings.Contains(out, "relay") {
		t.Errorf("cargo build (relay) output did not report the relay tier:\n%s", out)
	}

	// The offline build must have vendored the dependency in and compiled it — the seam worked.
	if s := limaShOn(t, sb.Name, "test -d "+proj+"/vendor/itoa && echo ok || echo no"); !strings.Contains(s, "ok") {
		t.Errorf("itoa was not vendored into the sandbox project (relay push failed): %q", s)
	}
	if s := limaShOn(t, sb.Name, "ls "+proj+"/target/*/deps/libitoa-*.rlib 2>/dev/null | head -1"); strings.TrimSpace(s) == "" {
		t.Errorf("the dependency did not compile offline (no libitoa rlib under target/deps)")
	}
}
