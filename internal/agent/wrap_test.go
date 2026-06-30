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
