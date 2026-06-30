package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

// wrapInstaller builds an Installer over a real local ExecFS rooted at a temp dir,
// so Wrap's writes and `command -v`/verify run for real (against the host shell).
func wrapInstaller(t *testing.T) (*Installer, string) {
	t.Helper()
	root := t.TempDir()
	r := remotefstest.LocalRunner{}
	return NewInstaller(Config{FS: remotefs.NewExecFS(r), Runner: r, Root: root}), root
}

// TestWrap_ExplicitBinNoRelay: wrapping with --bin writes the run launcher (baking
// that exact path), the nana dispatcher, and verifies — without relaying anything.
func TestWrap_ExplicitBinNoRelay(t *testing.T) {
	inst, root := wrapInstaller(t)
	d := Descriptor{Name: "t", DisplayName: "T", Bin: "echo", VersionArgs: []string{"ok"}, SystemPromptFlag: "--sys"}

	res, err := inst.Wrap(context.Background(), d, "", "/bin/echo")
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if res.Bin != "/bin/echo" {
		t.Errorf("res.Bin = %q, want the explicit /bin/echo", res.Bin)
	}
	runScript, err := os.ReadFile(filepath.Join(root, "agent", "t", "run"))
	if err != nil {
		t.Fatalf("run launcher not written: %v", err)
	}
	if !strings.Contains(string(runScript), "bin='/bin/echo'") {
		t.Errorf("run script should bake the explicit binary:\n%s", runScript)
	}
	if _, err := os.Stat(filepath.Join(root, "nana")); err != nil {
		t.Errorf("nana dispatcher not written: %v", err)
	}
	// No relay: the agent dir holds only our wrapper files, no relayed binary.
	if _, err := os.Stat(filepath.Join(root, "agent", "t", "echo")); !os.IsNotExist(err) {
		t.Errorf("wrap must not relay a binary into the agent dir")
	}
}

// TestWrap_ResolvesBinFromPath: with no --bin, the descriptor's Bin is resolved to
// an absolute path via `command -v` and baked in.
func TestWrap_ResolvesBinFromPath(t *testing.T) {
	inst, root := wrapInstaller(t)
	d := Descriptor{Name: "t", Bin: "ls", VersionArgs: []string{"/"}}

	res, err := inst.Wrap(context.Background(), d, "", "")
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if !filepath.IsAbs(res.Bin) || !strings.HasSuffix(res.Bin, "ls") {
		t.Errorf("res.Bin = %q, want an absolute path to ls", res.Bin)
	}
	runScript, _ := os.ReadFile(filepath.Join(root, "agent", "t", "run"))
	if !strings.Contains(string(runScript), "bin='"+res.Bin+"'") {
		t.Errorf("run script should bake the resolved absolute path %q:\n%s", res.Bin, runScript)
	}
}

func TestWrap_BinNotFound(t *testing.T) {
	inst, _ := wrapInstaller(t)
	d := Descriptor{Name: "t", Bin: "definitely-not-a-real-binary-xyz", VersionArgs: []string{"--version"}}
	if _, err := inst.Wrap(context.Background(), d, "", ""); err == nil || !strings.Contains(err.Error(), "--bin") {
		t.Fatalf("want a not-found error suggesting --bin, got %v", err)
	}
}

// TestWrap_WritesEnvWhenToken covers the wrap+token path (the rest of the suite
// uses --skip-auth): the 0600 env.sh is written with the token export.
func TestWrap_WritesEnvWhenToken(t *testing.T) {
	inst, root := wrapInstaller(t)
	d := Descriptor{Name: "t", Bin: "echo", TokenEnv: "TEST_TOKEN", VersionArgs: []string{"ok"}}

	res, err := inst.Wrap(context.Background(), d, "tok-abc", "/bin/echo")
	if err != nil {
		t.Fatal(err)
	}
	if !res.AuthConfigured {
		t.Error("AuthConfigured should be true when a token is given")
	}
	env := filepath.Join(root, "agent", "t", "env.sh")
	fi, err := os.Stat(env)
	if err != nil {
		t.Fatalf("env.sh not written: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("env.sh perms = %v, want 0600", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(env); !strings.Contains(string(b), "export TEST_TOKEN='tok-abc'") {
		t.Errorf("env.sh missing token export:\n%s", b)
	}
}

// TestWrap_RejectsRelativeBin enforces the documented absolute-path invariant.
func TestWrap_RejectsRelativeBin(t *testing.T) {
	inst, _ := wrapInstaller(t)
	d := Descriptor{Name: "t", Bin: "echo", VersionArgs: []string{"ok"}}
	if _, err := inst.Wrap(context.Background(), d, "", "bin/echo"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative --bin must be rejected, got %v", err)
	}
}

// TestWrap_VerifyFails: a binary that exists but exits non-zero fails the wrap with
// a clear error — after the launcher + nana were written (partial Result).
func TestWrap_VerifyFails(t *testing.T) {
	inst, _ := wrapInstaller(t)
	// `false` exits 1, so verify fails regardless of args.
	d := Descriptor{Name: "t", Bin: "false", VersionArgs: []string{"--version"}}
	res, err := inst.Wrap(context.Background(), d, "", "/usr/bin/false")
	if err == nil || !strings.Contains(err.Error(), "failed to run") {
		t.Fatalf("want a verify failure, got %v", err)
	}
	if res.Launcher == "" {
		t.Error("the launcher should still be recorded on a verify failure (partial Result)")
	}
}
