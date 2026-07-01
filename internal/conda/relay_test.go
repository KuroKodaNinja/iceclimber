package conda

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

// A trimmed but realistic `conda create --dry-run --json` solve: two LINK records (a
// noarch pure package and a linux-64 native one) with full download metadata.
const solveFixture = `Collecting package metadata (repodata.json): done
Solving environment: done
{
  "actions": {
    "LINK": [
      {"name":"six","version":"1.16.0","build":"pyh6c4a22f_0","build_number":0,"subdir":"noarch","fn":"six-1.16.0-pyh6c4a22f_0.tar.bz2","url":"https://conda.anaconda.org/conda-forge/noarch/six-1.16.0-pyh6c4a22f_0.tar.bz2","depends":["python"],"md5":"aa","sha256":"deadbeef","size":14,"license":"MIT"},
      {"name":"python","version":"3.12.1","build":"hab00c5b_0_cpython","build_number":0,"subdir":"linux-64","fn":"python-3.12.1-hab00c5b_0_cpython.conda","url":"https://conda.anaconda.org/conda-forge/linux-64/python-3.12.1-hab00c5b_0_cpython.conda","depends":["libgcc-ng >=12","openssl >=3.2,<4.0a0"],"md5":"bb","sha256":"cafef00d","size":31000000}
    ]
  },
  "dry_run": true,
  "success": true
}`

func TestParseSolveJSON(t *testing.T) {
	sv, err := parseSolveJSON([]byte(solveFixture))
	if err != nil {
		t.Fatalf("parseSolveJSON: %v", err)
	}
	if !sv.Success || len(sv.Actions.Link) != 2 {
		t.Fatalf("solve = %+v, want success with 2 LINK records", sv)
	}
	py := sv.Actions.Link[1]
	if py.Name != "python" || py.Subdir != "linux-64" || py.SHA256 != "cafef00d" {
		t.Errorf("python record = %+v", py)
	}
}

func TestRecordDerivation(t *testing.T) {
	// fn/subdir fall back to the URL when the fields are absent.
	r := condaRecord{Name: "six", Version: "1.16.0", Build: "x",
		URL: "https://conda.anaconda.org/conda-forge/noarch/six-1.16.0-x.tar.bz2"}
	if r.fn() != "six-1.16.0-x.tar.bz2" {
		t.Errorf("fn derived from URL = %q", r.fn())
	}
	if r.subdir() != "noarch" {
		t.Errorf("subdir derived from URL = %q", r.subdir())
	}
	// With no URL at all, fn is synthesized and subdir defaults to noarch.
	bare := condaRecord{Name: "n", Version: "1", Build: "b"}
	if bare.fn() != "n-1-b.conda" || bare.subdir() != "noarch" {
		t.Errorf("bare record fn=%q subdir=%q", bare.fn(), bare.subdir())
	}
}

func TestMergeRecords_BackfillsFromFetch(t *testing.T) {
	link := []condaRecord{{Name: "six", Version: "1.16.0", Build: "b", Subdir: "noarch"}}
	fetch := []condaRecord{{Name: "six", Version: "1.16.0", Build: "b",
		URL: "https://x/noarch/six-1.16.0-b.tar.bz2", SHA256: "abc", Size: 14}}
	got := mergeRecords(link, fetch)
	if len(got) != 1 {
		t.Fatalf("merged = %+v, want 1 record", got)
	}
	if got[0].URL == "" || got[0].SHA256 != "abc" || got[0].Size != 14 {
		t.Errorf("LINK record not backfilled from FETCH: %+v", got[0])
	}
	// With no LINK actions (fully cached solve) it falls back to FETCH.
	if fb := mergeRecords(nil, fetch); len(fb) != 1 || fb[0].URL == "" {
		t.Errorf("empty LINK should fall back to FETCH: %+v", fb)
	}
}

func TestSynthesizeRepodata_SplitsByExtension(t *testing.T) {
	sv, _ := parseSolveJSON([]byte(solveFixture))
	recs := mergeRecords(sv.Actions.Link, sv.Actions.Fetch)

	// noarch subdir: the .tar.bz2 six package lands in `packages`, not `packages.conda`.
	var noarch []condaRecord
	var linux64 []condaRecord
	for _, r := range recs {
		if r.subdir() == "noarch" {
			noarch = append(noarch, r)
		} else {
			linux64 = append(linux64, r)
		}
	}

	rdBytes, err := synthesizeRepodata("noarch", noarch)
	if err != nil {
		t.Fatalf("synthesizeRepodata: %v", err)
	}
	var rd repodata
	if err := json.Unmarshal(rdBytes, &rd); err != nil {
		t.Fatalf("repodata not valid JSON: %v", err)
	}
	if rd.Info.Subdir != "noarch" || rd.RepodataVersion != 1 {
		t.Errorf("repodata info = %+v", rd.Info)
	}
	if _, ok := rd.Packages["six-1.16.0-pyh6c4a22f_0.tar.bz2"]; !ok {
		t.Errorf(".tar.bz2 should be in `packages`: %v", rd.Packages)
	}
	if len(rd.PackagesConda) != 0 {
		t.Errorf("noarch has no .conda packages: %v", rd.PackagesConda)
	}
	// Both maps must always serialize (never null) so conda can index the channel.
	if !strings.Contains(string(rdBytes), `"packages.conda"`) || !strings.Contains(string(rdBytes), `"packages"`) {
		t.Errorf("repodata missing a package map:\n%s", rdBytes)
	}

	// linux-64: the python .conda file lands in `packages.conda` with its depends intact.
	rdBytes, _ = synthesizeRepodata("linux-64", linux64)
	_ = json.Unmarshal(rdBytes, &rd)
	entry, ok := rd.PackagesConda["python-3.12.1-hab00c5b_0_cpython.conda"]
	if !ok {
		t.Fatalf(".conda should be in `packages.conda`: %v", rd.PackagesConda)
	}
	if len(entry.Depends) != 2 || entry.Subdir != "linux-64" {
		t.Errorf("python entry = %+v", entry)
	}
}

func TestSynthesizeRepodata_NilDependsBecomesArray(t *testing.T) {
	// conda rejects a null `depends`; a record with no deps must serialize `[]`.
	rdBytes, err := synthesizeRepodata("noarch", []condaRecord{{Name: "n", Version: "1", Build: "b", Fn: "n-1-b.tar.bz2"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rdBytes), `"depends": []`) {
		t.Errorf("nil depends should serialize as []:\n%s", rdBytes)
	}
}

func TestControllerChannelArgs_DropsOffline(t *testing.T) {
	// Two-token, inline (-c=/--channel=), and --override-channels all forwarded; the
	// sandbox-only --offline/--use-local dropped from the controller solve.
	got := controllerChannelArgs([]string{"-c", "conda-forge", "--override-channels", "--offline",
		"--use-local", "--channel", "bioconda", "-c=defaults", "--channel=my-chan"})
	want := []string{"-c", "conda-forge", "--override-channels", "--channel", "bioconda", "-c=defaults", "--channel=my-chan"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("controllerChannelArgs = %v, want %v (offline/use-local dropped, inline kept)", got, want)
	}
}

func TestResolveTier(t *testing.T) {
	cases := []struct {
		tier  string
		extra []string
		want  string
	}{
		{"relay", nil, pkg.TierRelay},
		{"mirror", []string{"--offline"}, pkg.TierMirror}, // explicit mirror wins over --offline
		{"auto", []string{"--offline"}, pkg.TierRelay},    // auto escalates on --offline
		{"auto", []string{"-c", "conda-forge"}, pkg.TierMirror},
		{"", nil, pkg.TierMirror},
	}
	for _, c := range cases {
		if got := resolveTier(c.tier, c.extra); got != c.want {
			t.Errorf("resolveTier(%q, %v) = %q, want %q", c.tier, c.extra, got, c.want)
		}
	}
}

func TestResultOutcome_StampsTier(t *testing.T) {
	m := New(Config{})
	res := remote.Result{Stdout: []byte(`{"success":true,"actions":{"LINK":[{"name":"six","version":"1.17.0"}]}}`)}
	for _, tier := range []string{pkg.TierMirror, pkg.TierRelay} {
		out, err := m.resultOutcome(res, []pkg.Spec{{Name: "six"}}, tier)
		if err != nil {
			t.Fatalf("resultOutcome: %v", err)
		}
		if len(out.Installed) != 1 || out.Installed[0].Tier != tier || out.Installed[0].Version != "1.17.0" {
			t.Errorf("tier %q → %+v, want six 1.17.0 tagged %q", tier, out.Installed, tier)
		}
	}
}

func TestSafePathComponent(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "../etc"} {
		if err := safePathComponent(bad); err == nil {
			t.Errorf("safePathComponent(%q) = nil, want rejected", bad)
		}
	}
	for _, ok := range []string{"linux-64", "noarch", "six-1.17.0-pyhe01879c_1.conda"} {
		if err := safePathComponent(ok); err != nil {
			t.Errorf("safePathComponent(%q) = %v, want ok", ok, err)
		}
	}
}

func TestDownloadChannel(t *testing.T) {
	body := []byte("fake-conda-package-bytes")
	sum := sha256.Sum256(body)
	good := hex.EncodeToString(sum[:])
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	m := New(Config{HTTPClient: srv.Client()}) // trusts the test server's self-signed cert
	dir := t.TempDir()

	// Happy path: downloads, verifies the matching sha256, returns name→sha.
	shas, err := m.downloadChannel(context.Background(), dir,
		[]condaRecord{{Name: "six", Version: "1", Subdir: "noarch", Fn: "six-1-b.conda", URL: srv.URL + "/six-1-b.conda", SHA256: good}})
	if err != nil {
		t.Fatalf("downloadChannel: %v", err)
	}
	if shas["six"] != good {
		t.Errorf("sha map = %v, want six→%s", shas, good)
	}

	// sha256 mismatch is rejected.
	if _, err := m.downloadChannel(context.Background(), t.TempDir(),
		[]condaRecord{{Name: "six", Fn: "six-1-b.conda", URL: srv.URL + "/x.conda", SHA256: "deadbeef"}}); err == nil {
		t.Error("sha256 mismatch should error")
	}

	// A traversal-laden fn is rejected before any write (path.Base already strips it, but
	// the guard is belt-and-suspenders).
	if _, err := m.downloadChannel(context.Background(), t.TempDir(),
		[]condaRecord{{Name: "x", Subdir: "..", URL: srv.URL + "/x", SHA256: good}}); err == nil {
		t.Error("unsafe subdir should error")
	}

	// Non-https URL is refused.
	if _, err := m.downloadChannel(context.Background(), t.TempDir(),
		[]condaRecord{{Name: "x", Fn: "x.conda", URL: "http://insecure/x.conda"}}); err == nil {
		t.Error("non-https URL should be refused")
	}
}

func TestCondaSubdir(t *testing.T) {
	for _, c := range []struct{ arch, want string }{
		{"amd64", "linux-64"}, {"x86_64", "linux-64"},
		{"arm64", "linux-aarch64"}, {"aarch64", "linux-aarch64"},
	} {
		if got := condaSubdir(c.arch, "glibc"); got != c.want {
			t.Errorf("condaSubdir(%q) = %q, want %q", c.arch, got, c.want)
		}
	}
}

func TestOfflineEnvCmd(t *testing.T) {
	m := New(Config{CondaBin: "/opt/conda/bin/conda", EnvPrefix: "/root/envs/conda-python-3.12"})
	// Fresh env → `conda create ... python=<minor>`.
	create := m.offlineEnvCmd("/root/envs/conda-python-3.12", "file:///root/blobs/conda-chan-x", "3.12",
		[]pkg.Spec{{Name: "six"}}, false)
	for _, want := range []string{"create", "-y", "--json", "--offline", "--override-channels",
		"--solver classic", "file:///root/blobs/conda-chan-x", "python=3.12", "six"} {
		if !strings.Contains(create, want) {
			t.Errorf("offlineEnvCmd(create) missing %q:\n%s", want, create)
		}
	}
	// Existing env → `conda install ... <specs>` and NO create / NO python= pin (python is
	// already present; create would refuse the populated prefix).
	install := m.offlineEnvCmd("/root/envs/conda-python-3.12", "file:///c", "3.12", []pkg.Spec{{Name: "six"}}, true)
	if !strings.Contains(install, " install ") {
		t.Errorf("offlineEnvCmd(exists) should use `install`:\n%s", install)
	}
	if strings.Contains(install, " create ") || strings.Contains(install, "python=3.12") {
		t.Errorf("offlineEnvCmd(exists) must not create or re-pin python:\n%s", install)
	}
}
