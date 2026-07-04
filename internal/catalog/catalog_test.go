package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedCatalogLoadsAndValidates: the shipped defaults parse, validate, and cover
// the three kind/recipe combinations the design requires (static-pinned, github-release
// +shasums-file, github-release+pinned).
func TestEmbeddedCatalogLoadsAndValidates(t *testing.T) {
	c, err := Load(nil)
	if err != nil {
		t.Fatalf("embedded catalog must load clean: %v", err)
	}
	for _, name := range []string{"bash", "jq", "fd"} {
		e, ok := c.Lookup(name)
		if !ok {
			t.Fatalf("embedded catalog missing %q", name)
		}
		if e.Source() != "embedded" {
			t.Errorf("%s source = %q, want embedded", name, e.Source())
		}
		if err := e.validate(); err != nil {
			t.Errorf("embedded %s invalid: %v", name, err)
		}
	}
	// The recipe coverage the design pins.
	if c.byName["bash"].effectiveRecipe() != recipePinned {
		t.Error("bash should resolve to the pinned recipe (static-pinned implies it)")
	}
	if c.byName["jq"].Checksums.Recipe != recipeShasumsFile {
		t.Error("jq should use the shasums-file recipe")
	}
	if c.byName["fd"].Checksums.Recipe != recipePinned {
		t.Error("fd should use the pinned recipe (upstream ships no sums)")
	}
}

// TestResolve covers all three kinds against the embedded entries. static-pinned + pinned
// need no network; shasums-file fetches a sums file (a local server stands in for GitHub).
func TestResolve(t *testing.T) {
	ctx := context.Background()
	c, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}

	// bash: static-pinned → URL + inline sha, no network.
	bash, _ := c.Lookup("bash")
	r, err := bash.Resolve(ctx, nil, "linux", "aarch64", "musl")
	if err != nil {
		t.Fatalf("bash resolve: %v", err)
	}
	if !strings.HasPrefix(r.Asset.URL, "https://github.com/robxu9/bash-static/") || len(r.Asset.SHA256) != 64 {
		t.Errorf("bash resolved = %+v", r.Asset)
	}
	if !strings.HasPrefix(r.Asset.Name, "tool-bash-5.2.15-") {
		t.Errorf("cache name should namespace tool+version: %q", r.Asset.Name)
	}
	// glibc gets the SAME static musl binary (both rows point at it).
	rg, _ := bash.Resolve(ctx, nil, "linux", "aarch64", "glibc")
	if rg.Asset.URL != r.Asset.URL {
		t.Error("bash musl-static should serve glibc from the same asset")
	}

	// fd: github-release + pinned → composed release URL, inline sha, no network.
	fd, _ := c.Lookup("fd")
	rf, err := fd.Resolve(ctx, nil, "linux", "x86_64", "glibc")
	if err != nil {
		t.Fatalf("fd resolve: %v", err)
	}
	if !strings.Contains(rf.Asset.URL, "/sharkdp/fd/releases/download/v10.4.2/fd-v10.4.2-x86_64-unknown-linux-musl.tar.gz") {
		t.Errorf("fd release URL wrong: %s", rf.Asset.URL)
	}
	if rf.Format != "tar.gz" {
		t.Errorf("fd format = %q, want tar.gz", rf.Format)
	}

	// jq: github-release + shasums-file → the sha comes from a fetched sums file.
	realSha := "8b85c817833814ddca00a144c33705546355afccf0cf39b188f3cdb48b852309"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "sha256sum.txt") {
			// Two-column format, exactly like jq's upstream file.
			_, _ = w.Write([]byte("deadbeef  jq-linux-amd64\n" + realSha + "  jq-linux-arm64\n"))
			return
		}
		http.NotFound(w, req)
	}))
	defer srv.Close()

	// fetchSum against a shasums-index entry pointed at the local server — exercises the
	// real HTTP fetch + two-column parse end to end (github-release composes the same way,
	// covered by fd's URL above; the fetch/parse seam is what shasums-file adds).
	idx := Entry{
		Name: "probe", Kind: KindShasumsIndex, BaseURL: srv.URL,
		Checksums: Checksums{Recipe: recipeShasumsFile, Asset: "sha256sum.txt"},
	}
	if sha, err := idx.fetchSum(ctx, srv.Client(), Platform{Asset: "jq-linux-arm64"}); err != nil || sha != realSha {
		t.Errorf("fetchSum over http = %q, %v; want %q", sha, err, realSha)
	}
	if _, err := idx.fetchSum(ctx, srv.Client(), Platform{Asset: "absent"}); err == nil {
		t.Error("fetchSum should error when the asset is absent from the sums file")
	}

	got := sumFor("deadbeef  jq-linux-amd64\n"+realSha+"  jq-linux-arm64\n", "jq-linux-arm64")
	if got != realSha {
		t.Errorf("sumFor = %q, want %q", got, realSha)
	}
	if sumFor("x  other\n", "jq-linux-arm64") != "" {
		t.Error("sumFor should return empty when the asset is absent")
	}
	// A binary-mode "*" marker and a path prefix are tolerated.
	if sumFor(realSha+" *dist/jq-linux-arm64\n", "jq-linux-arm64") != realSha {
		t.Error("sumFor should tolerate a *binary-mode marker and a path prefix")
	}

	// A fingerprint with no row is a clear error, not a panic.
	if _, err := bash.Resolve(ctx, nil, "linux", "riscv64", "musl"); err == nil || !strings.Contains(err.Error(), "no build") {
		t.Errorf("missing platform should error clearly, got %v", err)
	}
}

// TestOverlayOverrideAndCollisions: an operator file overrides an embedded entry by name;
// two operator files defining one name is a hard error naming both; a malformed entry names
// its file and fails the load.
func TestOverlayOverrideAndCollisions(t *testing.T) {
	dir := t.TempDir()
	// Override jq to a fictional pinned build.
	over := filepath.Join(dir, "mine.yaml")
	os.WriteFile(over, []byte(`
- name: jq
  description: my pinned jq
  kind: static-pinned
  version: "1.7.1"
  platforms:
    linux/x86_64/glibc: {url: "https://mirror.corp/jq", sha256: `+strings.Repeat("a", 64)+`}
  unpack: {format: binary, bins: [jq]}
  verify: "jq --version"
`), 0o644)

	c, err := Load([]string{over})
	if err != nil {
		t.Fatalf("overlay load: %v", err)
	}
	jq, _ := c.Lookup("jq")
	if jq.Version != "1.7.1" || jq.Source() != over {
		t.Errorf("operator entry should override embedded: version=%q source=%q", jq.Version, jq.Source())
	}
	// The other embedded entries survive.
	if _, ok := c.Lookup("bash"); !ok {
		t.Error("overriding one tool must not drop the rest of the embedded set")
	}

	// Two operator FILES defining one name → hard error naming both (via the second file).
	dupA := filepath.Join(dir, "a.yaml")
	dupB := filepath.Join(dir, "b.yaml")
	body := "- {name: dupe, kind: static-pinned, version: \"1\", platforms: {linux/x86_64/glibc: {url: u, sha256: " + strings.Repeat("b", 64) + "}}, unpack: {format: binary, bins: [dupe]}, verify: dupe}\n"
	os.WriteFile(dupA, []byte(body), 0o644)
	os.WriteFile(dupB, []byte(body+body), 0o644) // b.yaml defines dupe TWICE
	if _, err := Load([]string{dupB}); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("a name defined twice in one file should error, got %v", err)
	}

	// A malformed operator entry names its file.
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(bad, []byte("- {name: broken, kind: github-release, version: \"1\", unpack: {format: binary, bins: [x]}, verify: x}\n"), 0o644)
	if _, err := Load([]string{bad}); err == nil || !strings.Contains(err.Error(), bad) {
		t.Errorf("a malformed entry should error naming its file, got %v", err)
	}
}
