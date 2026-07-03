package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cacheFixture builds a cache dir with an old asset (+lock), a fresh asset, and a stale
// .tmp stray, and points the package config at it. Returns the dir.
func cacheFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, age time.Duration, size int) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, bytes.Repeat([]byte("x"), size), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	write("node-v24.tar.gz", 40*24*time.Hour, 4096)
	write("node-v24.tar.gz.lock", 40*24*time.Hour, 0)
	write("bash-linux-arm64", time.Hour, 2048)
	write("chan-pkg.conda.tmp", 30*24*time.Hour, 512) // interrupted download stray

	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "iceclimber.yaml")
	if err := os.WriteFile(cfgPath, []byte("sandbox_id: c\nssh: {host: h, user: u}\ncache_dir: "+dir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = old })
	return dir
}

// TestCacheListAndPrune drives the real commands over a fixture cache: list shows assets
// (not locks) with a total and --json parses; prune --older-than removes only old assets
// (+ their lock companions) and stale strays, keeping fresh entries; --yes is required
// off-terminal.
func TestCacheListAndPrune(t *testing.T) {
	dir := cacheFixture(t)

	// list (human): both assets + the stale stray, no .lock line, a total.
	list := newCacheListCmd()
	var out bytes.Buffer
	list.SetOut(&out)
	list.SetArgs(nil)
	if err := list.Execute(); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"node-v24.tar.gz", "bash-linux-arm64", "(stale)", "total:"} {
		if !strings.Contains(s, want) {
			t.Errorf("list missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, ".lock") {
		t.Errorf("list must not show lock companions:\n%s", s)
	}

	// list --json parses and carries sizes.
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	out.Reset()
	list2 := newCacheListCmd()
	list2.SetOut(&out)
	list2.SetArgs(nil)
	if err := list2.Execute(); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Dir     string       `json:"dir"`
		Total   int64        `json:"total_bytes"`
		Entries []cacheEntry `json:"entries"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("list --json unparseable: %v\n%s", err, out.String())
	}
	if parsed.Dir != dir || parsed.Total != 4096+2048+512 || len(parsed.Entries) != 3 {
		t.Errorf("json = dir %q total %d entries %d; want %q/%d/3", parsed.Dir, parsed.Total, len(parsed.Entries), dir, 4096+2048+512)
	}
	jsonOutput = false

	// Interactive decline: answering "n" at the confirmation aborts without deleting.
	// (The scripted no-tty refusal is covered functionally — TestCachePruneScripted —
	// since in-process stdin may be a char device like /dev/null and fool the tty check.)
	out.Reset()
	decline := newCachePruneCmd()
	decline.SetOut(&out)
	decline.SetIn(strings.NewReader("n\n"))
	decline.SetArgs(nil)
	if isTerminal(os.Stdin) { // only meaningful where the guard lets confirm run
		if err := decline.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "aborted") {
			t.Errorf("declined prune should abort:\n%s", out.String())
		}
		if _, err := os.Stat(filepath.Join(dir, "node-v24.tar.gz")); err != nil {
			t.Error("declined prune must not delete anything")
		}
	}

	// prune --older-than 30d --yes: removes the old asset + its lock + the stale stray,
	// keeps the fresh asset.
	out.Reset()
	prune := newCachePruneCmd()
	prune.SetOut(&out)
	prune.SetArgs([]string{"--older-than", "720h", "--yes"})
	if err := prune.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pruned 2 artifact(s)") {
		t.Errorf("prune summary wrong:\n%s", out.String())
	}
	for _, gone := range []string{"node-v24.tar.gz", "node-v24.tar.gz.lock", "chan-pkg.conda.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should have been pruned", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "bash-linux-arm64")); err != nil {
		t.Errorf("fresh asset must survive an --older-than prune: %v", err)
	}
}
