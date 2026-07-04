package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// fakeTool is a runnable stand-in binary: a shell script answering --version.
func fakeTool(name string) []byte {
	return []byte("#!/bin/sh\necho '" + name + " 1.0 (test)'\n")
}

// tgz packs body as <top>/<name> in a gzip'd tar (the strip-1 layout real tool archives use).
func tgz(t *testing.T, top, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, h := range []*tar.Header{
		{Name: top + "/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: top + "/" + name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write(body)
		}
	}
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

// TestInstall_BinaryAndTarGz drives real installs over a local ExecFS: a static-pinned
// single binary and a github-release tar.gz (pinned), both fetched from a local server,
// pushed, linked into tools/bin, chmod'd, and verified by running --version. Then a
// second install is idempotent, and a version bump upgrades in place.
func TestInstall_BinaryAndTarGz(t *testing.T) {
	ctx := context.Background()
	jqBin := fakeTool("jq")
	fdArchive := tgz(t, "fd-top", "fd", fakeTool("fd"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/jq":
			w.Write(jqBin)
		case r.URL.Path == "/fd.tar.gz":
			w.Write(fdArchive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	deps := InstallDeps{
		FS:     remotefs.NewExecFS(remotefstest.LocalRunner{}),
		Runner: remotefstest.LocalRunner{},
		Root:   root,
		OS:     "linux", Arch: "x86_64", Libc: "glibc",
		CacheDir: filepath.Join(t.TempDir(), "cache"),
		Progress: func(progress.Event) {},
	}

	// static-pinned single binary.
	jq := Entry{
		Name: "jq", Kind: KindStaticPinned, Version: "1.0",
		Platforms: map[string]Platform{"linux/x86_64/glibc": {URL: srv.URL + "/jq", SHA256: sha(jqBin)}},
		Unpack:    Unpack{Format: "binary", Bins: []string{"jq"}},
		Verify:    "jq --version",
	}
	res, err := jq.Install(ctx, deps)
	if err != nil {
		t.Fatalf("install jq: %v", err)
	}
	if res.AlreadyInstalled {
		t.Error("first install should not be AlreadyInstalled")
	}
	// The bin is linked under tools/bin and runs.
	link := filepath.Join(root, "tools", "bin", "jq")
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("jq should be a symlink under tools/bin: %v", err)
	}
	if got := runLocal(t, link, "--version"); got != "jq 1.0 (test)\n" {
		t.Errorf("linked jq ran wrong: %q", got)
	}

	// github-release tar.gz (pinned), strip-1.
	fd := Entry{
		Name: "fd", Kind: KindGithubRelease, Version: "1.0", Repo: "sharkdp/fd", ReleaseTag: "v1.0",
		Checksums: Checksums{Recipe: recipePinned},
		// Kind is github-release but we point the URL at the local server via a static
		// URL override isn't available — so use shasums-index style base_url instead:
		Platforms: map[string]Platform{"linux/x86_64/glibc": {Asset: "fd.tar.gz", SHA256: sha(fdArchive)}},
		Unpack:    Unpack{Format: "tar.gz", Strip: 1, Bins: []string{"fd"}},
		Verify:    "fd --version",
	}
	fd.Kind = KindShasumsIndex // shasums-index composes URL from base_url (local server)
	fd.BaseURL = srv.URL
	fd.Checksums.Recipe = recipePinned
	if _, err := fd.Install(ctx, deps); err != nil {
		t.Fatalf("install fd: %v", err)
	}
	fdLink := filepath.Join(root, "tools", "bin", "fd")
	if got := runLocal(t, fdLink, "--version"); got != "fd 1.0 (test)\n" {
		t.Errorf("linked fd ran wrong: %q", got)
	}

	// Idempotent: reinstalling jq (present + verifying) reports AlreadyInstalled, no refetch.
	res2, err := jq.Install(ctx, deps)
	if err != nil || !res2.AlreadyInstalled {
		t.Errorf("second jq install should be idempotent: %+v %v", res2, err)
	}

	// Upgrade: a new version replaces the install dir wholesale and re-links.
	jqV2 := jq
	jqV2.Version = "2.0"
	jqBinV2 := fakeTool("jq2.0")
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(jqBinV2) }))
	defer srv2.Close()
	jqV2.Platforms = map[string]Platform{"linux/x86_64/glibc": {URL: srv2.URL + "/jq", SHA256: sha(jqBinV2)}}
	if res, err := jqV2.Install(ctx, deps); err != nil || res.AlreadyInstalled {
		t.Fatalf("upgrade install should re-run, not report AlreadyInstalled: %+v %v", res, err)
	}
	if got := runLocal(t, link, "--version"); got != "jq2.0 1.0 (test)\n" {
		t.Errorf("upgrade did not replace the binary: %q", got)
	}
}

// TestLoad_BinNameCollision: two tools that both link the same binary name into tools/bin
// are rejected at load (a catalog-consistency error), not left to clobber at install.
func TestLoad_BinNameCollision(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "conflict.yaml")
	os.WriteFile(f, []byte(`
- {name: toola, kind: static-pinned, version: "1", platforms: {linux/x86_64/glibc: {url: u, sha256: `+seq("a")+`}}, unpack: {format: binary, bins: [shared]}, verify: shared}
- {name: toolb, kind: static-pinned, version: "1", platforms: {linux/x86_64/glibc: {url: u, sha256: `+seq("b")+`}}, unpack: {format: binary, bins: [shared]}, verify: shared}
`), 0o644)
	_, err := Load([]string{f})
	if err == nil || !contains(err.Error(), "shared") || !contains(err.Error(), "tools/bin") {
		t.Errorf("bin-name collision should be a load error naming the bin + tools/bin, got %v", err)
	}
}

func seq(c string) string {
	out := ""
	for i := 0; i < 64; i++ {
		out += c
	}
	return out
}
func contains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }

func runLocal(t *testing.T, bin string, args ...string) string {
	t.Helper()
	res, err := remotefstest.LocalRunner{}.Run(context.Background(), shellQuote(bin)+" "+argsJoin(args), nil)
	if err != nil {
		t.Fatalf("run %s: %v", bin, err)
	}
	return string(res.Stdout)
}

func shellQuote(s string) string { return "'" + s + "'" }
func argsJoin(a []string) string {
	out := ""
	for i, s := range a {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
