//go:build functional

package functional

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestCargoBuildRelay validates the cargo.build package-manager seam on BOTH boxes via the
// air-gapped relay tier: the controller's cargo vendors a crate dependency, Popo pushes the
// vendor tree + a source-replacement .cargo/config.toml into the sandbox, and the sandbox builds
// OFFLINE against it.
//
// The two boxes exercise different depths on purpose:
//   - musl (Alpine): builds a BINARY end to end. The box has no C toolchain, but rust bundles
//     rust-lld and musl links self-contained, so cargo.build points the linker at rust-lld and a
//     static executable links + runs — the "resolve the linker toolchain" path.
//   - glibc (Ubuntu): builds a LIBRARY crate (rlib, no final link step). The gnu target is NOT
//     self-contained, so a binary would need a system cc the box lacks; a lib build still proves
//     the whole seam (resolve → vendor → relay → offline compile) without a linker.
func TestCargoBuildRelay(t *testing.T) {
	// Relay mode resolves + vendors on the controller (controller_cargo, like pip's
	// controller_python) — skip with a clear message where the controller lacks rust.
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not found on the controller; install rust to run the cargo relay test")
	}
	musl := requireSandbox(t)
	t.Run("musl-binary", func(t *testing.T) { cargoBuildRelayOn(t, musl, true) })
	t.Run("glibc-library", func(t *testing.T) { cargoBuildRelayOn(t, requireGlibcSandbox(t), false) })
}

func cargoBuildRelayOn(t *testing.T, sb sandboxConn, binary bool) {
	root := "/tmp/iceclimber-cargo-" + protocol.NewID()
	// egress_mode: relay → cargo.build takes the vendored offline path (no proxy/serve needed).
	cfg := writeYAML(t, sshConfigYAML(sb)+fmt.Sprintf("remote_root: %s\negress_mode: relay\n", root))
	scheduleRootCleanupOn(t, sb.Name, root)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "exec")
	runIceclimber(t, "install", "rust", "--config", cfg, "--transport", "exec")

	// A minimal crate with one small, pure-Rust, build-script-free dependency (itoa) — enough to
	// prove real crate resolution + offline compile. A binary additionally exercises linking.
	proj := root + "/proj"
	manifest := "[package]\nname = \"probe\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\nitoa = \"1\"\n"
	var srcFile, src string
	if binary {
		srcFile, src = "src/main.rs", "fn main(){ let mut b = itoa::Buffer::new(); println!(\"n={}\", b.format(42u32)); }\n"
	} else {
		manifest += "\n[lib]\npath = \"src/lib.rs\"\n"
		srcFile, src = "src/lib.rs", "pub fn n() -> String { let mut b = itoa::Buffer::new(); b.format(42u32).to_string() }\n"
	}
	limaShOn(t, sb.Name, "mkdir -p "+proj+"/src && "+
		"printf '%s' "+shellSingleQuote(manifest)+" > "+proj+"/Cargo.toml && "+
		"printf '%s' "+shellSingleQuote(src)+" > "+proj+"/"+srcFile)

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
	if binary {
		// The static musl binary linked (via rust-lld, no system toolchain) and runs on the box.
		bin := strings.TrimSpace(limaShOn(t, sb.Name, "ls "+proj+"/target/release/probe 2>/dev/null | head -1"))
		if bin == "" {
			t.Fatal("no release binary produced — the rust-lld link step failed")
		}
		if v := limaShOn(t, sb.Name, remoteQuote(bin)+" 2>&1"); !strings.Contains(v, "n=42") {
			t.Errorf("the built binary did not run correctly: %q", strings.TrimSpace(v))
		}
	}
}

// shellSingleQuote wraps s for a POSIX single-quoted shell word (for printf '%s' <s>).
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
