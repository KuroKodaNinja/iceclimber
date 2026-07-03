package shell

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
)

func TestResolve(t *testing.T) {
	i := &Installer{cfg: Config{OS: "linux", Arch: "aarch64"}}
	r, err := i.resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if r.AssetName != "bash-linux-aarch64" || r.Version != bashVersion || len(r.SHA256) != 64 {
		t.Errorf("resolve = %+v, want the pinned aarch64 asset with a 64-hex sha", r)
	}
	// Every pinned arch carries a full sha256.
	for arch, a := range pinnedBash {
		if len(a.sha256) != 64 {
			t.Errorf("pinned %s sha256 = %q, want 64 hex chars", arch, a.sha256)
		}
	}
	// Unsupported OS / arch are clear errors, not a bogus asset.
	if _, err := (&Installer{cfg: Config{OS: "darwin", Arch: "aarch64"}}).resolve(); err == nil {
		t.Error("resolve should reject a non-linux OS")
	}
	if _, err := (&Installer{cfg: Config{OS: "linux", Arch: "riscv64"}}).resolve(); err == nil {
		t.Error("resolve should reject an unknown arch")
	}
}

func TestRenderRC_And_EnvPreamble(t *testing.T) {
	rc := renderRC("/srv/ice")
	for _, want := range []string{
		"export ICECLIMBER_HOME='/srv/ice'",
		`"$ICECLIMBER_HOME"/runtimes/*/*/bin`,           // installed runtimes on PATH
		`$ICECLIMBER_HOME/shell:$ICECLIMBER_HOME:$PATH`, // bash + popo/nana on PATH
		"egress-env.sh", // proxy/CA trust when serving
		`HISTFILE="$ICECLIMBER_HOME/shell/.bash_history"`,
		"completion-ignore-case on",
		`PS1='[iceclimber \w]\$ '`,
	} {
		if !strings.Contains(rc, want) {
			t.Errorf("rc missing %q:\n%s", want, rc)
		}
	}
	// EnvPreamble is POSIX-sh (used by the no-bash / -c fallbacks) and single-sources the env.
	if pre := EnvPreamble("/srv/ice"); !strings.Contains(rc, pre) {
		t.Errorf("rc should embed EnvPreamble verbatim:\npreamble=%s\nrc=%s", pre, rc)
	}
}

// TestInstall_PushesVerifiesAndDropsRC drives a full install over a real (local) ExecFS
// against a fake bash served by an httptest server: the binary is pushed executable, verified
// (it runs `--version`), the rc is dropped, and a second install is idempotent (rc refreshed).
func TestInstall_PushesVerifiesAndDropsRC(t *testing.T) {
	// A stand-in "bash": a shell script that answers --version, so verify() (which runs the
	// pushed file over the local runner) succeeds.
	fakeBash := []byte("#!/bin/sh\necho 'GNU bash, version 5.2.15 (test)'\n")
	sum := sha256.Sum256(fakeBash)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fakeBash)
	}))
	defer srv.Close()

	// Point resolve() at the test server + inject a test arch with the fake's checksum.
	origURL, origPin := bashBaseURL, pinnedBash["testarch"]
	bashBaseURL = srv.URL
	pinnedBash["testarch"] = bashAsset{name: "bash-test", sha256: hex.EncodeToString(sum[:])}
	t.Cleanup(func() { bashBaseURL = origURL; delete(pinnedBash, "testarch"); _ = origPin })

	root := t.TempDir()
	inst := NewInstaller(Config{
		FS:       remotefs.NewExecFS(remotefstest.LocalRunner{}),
		Runner:   remotefstest.LocalRunner{},
		Root:     root,
		OS:       "linux",
		Arch:     "testarch",
		CacheDir: filepath.Join(t.TempDir(), "cache"),
		Progress: func(progress.Event) {},
	})

	res, err := inst.Install(context.Background())
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.AlreadyInstalled {
		t.Error("first install should not report AlreadyInstalled")
	}
	if res.Path != BinPath(root) || res.RC != RCPath(root) {
		t.Errorf("result paths = %+v, want bin %s / rc %s", res, BinPath(root), RCPath(root))
	}
	// The bash landed, is executable, and matches the downloaded bytes.
	fi, err := os.Stat(BinPath(root))
	if err != nil {
		t.Fatalf("stat bash: %v", err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Errorf("bash mode = %v, want executable", fi.Mode())
	}
	if got, _ := os.ReadFile(BinPath(root)); string(got) != string(fakeBash) {
		t.Error("pushed bash bytes differ from the downloaded asset")
	}
	if got, _ := os.ReadFile(RCPath(root)); !strings.Contains(string(got), "ICECLIMBER_HOME") {
		t.Error("rc file missing or empty")
	}

	// Second install is idempotent: bash left as-is, rc refreshed.
	res2, err := inst.Install(context.Background())
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if !res2.AlreadyInstalled {
		t.Error("second install should report AlreadyInstalled")
	}
}
