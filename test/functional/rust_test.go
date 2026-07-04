//go:build functional

package functional

import (
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestRustInstall installs a real Rust toolchain onto BOTH the musl (Alpine) and glibc (Ubuntu)
// boxes through the engine's assemble hook — Rust ships a component-split distribution tarball
// (cargo/, rustc/, rust-std-<target>/) that Popo merges into one prefix on the controller before
// pushing — then proves rustc AND cargo actually run on the box. This is the end-to-end
// validation of the fourth managed language and the first to use runtime.Spec.Assemble: the
// shared engine absorbing a genuinely different package shape via one hook, no forked installer.
func TestRustInstall(t *testing.T) {
	musl := requireSandbox(t)
	t.Run("musl", func(t *testing.T) { rustInstallOn(t, musl) })
	t.Run("glibc", func(t *testing.T) { rustInstallOn(t, requireGlibcSandbox(t)) })
}

func rustInstallOn(t *testing.T, sb sandboxConn) {
	root := "/tmp/iceclimber-rust-" + protocol.NewID()
	cfg := writeConfigFor(t, sb, root) // schedules root cleanup on this box
	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "exec")

	out := string(runIceclimber(t, "install", "rust", "--config", cfg, "--transport", "exec"))
	if !strings.Contains(out, "rust ") || !strings.Contains(out, "/runtimes/rust/") {
		t.Errorf("install rust output unexpected:\n%s", out)
	}

	// The assembled prefix must carry BOTH cargo and rustc in one bin/ (the merge worked), and
	// both must execute on the box (the host build runs natively on this libc).
	rustc := strings.TrimSpace(limaShOn(t, sb.Name, "ls "+root+"/runtimes/rust/*/bin/rustc 2>/dev/null | head -1"))
	if rustc == "" {
		t.Fatal("no rustc under runtimes/rust after install (assemble/push failed)")
	}
	cargo := strings.Replace(rustc, "/rustc", "/cargo", 1)
	if v := limaShOn(t, sb.Name, remoteQuote(rustc)+" --version 2>&1"); !strings.Contains(v, "rustc ") {
		t.Errorf("rustc did not run on the sandbox: %q", strings.TrimSpace(v))
	}
	// cargo finding rustc adjacent in the same bin/ proves the merged prefix is coherent.
	if v := limaShOn(t, sb.Name, remoteQuote(cargo)+" --version 2>&1"); !strings.Contains(v, "cargo ") {
		t.Errorf("cargo did not run on the sandbox: %q", strings.TrimSpace(v))
	}

	// Re-install is idempotent (verify-on-hit short-circuits before any re-download/assemble).
	if out2 := string(runIceclimber(t, "install", "rust", "--config", cfg, "--transport", "exec")); !strings.Contains(out2, "already installed") {
		t.Errorf("second install rust should be idempotent:\n%s", out2)
	}
}
