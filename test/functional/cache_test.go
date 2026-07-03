//go:build functional

package functional

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCachePruneScripted pins the destructive-op contract with a REAL non-tty stdin
// (a pipe — in-process unit tests can't fake this): `cache prune` without --yes must be
// refused with guidance, and with --yes it must prune, over the real binary.
func TestCachePruneScripted(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "asset.tar.gz"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "iceclimber.yaml")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("sandbox_id: cachetest\nssh: {host: h, user: u}\ncache_dir: %s\n", dir)), 0o600); err != nil {
		t.Fatal(err)
	}

	// No --yes + piped stdin → refused, naming the flag.
	refuse := exec.Command(iceclimberBin, "cache", "prune", "--config", cfg)
	refuse.Stdin = strings.NewReader("")
	out, err := refuse.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "--yes") {
		t.Fatalf("scripted prune without --yes should be refused: err=%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "asset.tar.gz")); err != nil {
		t.Fatal("refused prune must not delete anything")
	}

	// --yes → pruned.
	allow := exec.Command(iceclimberBin, "cache", "prune", "--yes", "--config", cfg)
	out, err = allow.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "pruned 1 artifact(s)") {
		t.Fatalf("prune --yes: err=%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "asset.tar.gz")); !os.IsNotExist(err) {
		t.Error("asset should be gone after prune --yes")
	}
}
