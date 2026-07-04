package cargo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

func TestBuildHandlerErrors(t *testing.T) {
	h := BuildHandler(Deps{})
	if r := h(context.Background(), protocol.Request{ID: "1", Params: json.RawMessage("{")}); r.Error == nil || r.Error.Code != "malformed_params" {
		t.Errorf("malformed params: got %+v", r.Error)
	}
	if r := h(context.Background(), protocol.Request{ID: "2", Params: json.RawMessage(`{}`)}); r.Error == nil || r.Error.Code != "missing_project" {
		t.Errorf("missing project: got %+v", r.Error)
	}
}

func TestBuildParamsParse(t *testing.T) {
	var p buildParams
	if err := json.Unmarshal([]byte(`{"project":"/work/app","rust_version":"1.83.0","args":["--features","x"]}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Project != "/work/app" || p.RustVersion != "1.83.0" || len(p.Args) != 2 || p.Args[1] != "x" {
		t.Errorf("parsed %+v", p)
	}
	// Minimal: only project.
	p = buildParams{}
	if err := json.Unmarshal([]byte(`{"project":"/p"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Project != "/p" || p.RustVersion != "" || p.Args != nil {
		t.Errorf("minimal parsed %+v", p)
	}
}

func TestParseArtifacts(t *testing.T) {
	// A realistic cargo --message-format=json stream: a lib compiler-artifact (executable null),
	// a build-script-executed line, then the binary compiler-artifact (executable set), plus a
	// trailing non-JSON line and a duplicate to prove dedup + sort.
	stream := strings.Join([]string{
		`{"reason":"compiler-artifact","package_id":"lib 0.1.0","executable":null,"target":{"kind":["lib"]}}`,
		`{"reason":"build-script-executed","package_id":"dep 0.1.0"}`,
		`{"reason":"compiler-artifact","package_id":"app 0.1.0","executable":"/work/app/target/release/app","target":{"kind":["bin"]}}`,
		`{"reason":"compiler-artifact","package_id":"app 0.1.0","executable":"/work/app/target/release/app"}`,
		`{"reason":"compiler-artifact","package_id":"helper 0.1.0","executable":"/work/app/target/release/helper"}`,
		`{"reason":"build-finished","success":true}`,
		`   `,
		`some human line`,
	}, "\n")
	got := parseArtifacts([]byte(stream))
	want := []string{"/work/app/target/release/app", "/work/app/target/release/helper"}
	if len(got) != len(want) {
		t.Fatalf("parseArtifacts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("artifact[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if len(parseArtifacts(nil)) != 0 {
		t.Error("parseArtifacts(nil) should be empty")
	}
}

func TestRewriteVendorConfig(t *testing.T) {
	// cargo vendor's real printed snippet points `directory` at the controller-side vendor path;
	// the rewrite must repoint it at the sandbox vendor dir while keeping the replacement wiring.
	snippet := `[source.crates-io]
replace-with = "vendored-sources"

[source.vendored-sources]
directory = "/tmp/iceclimber-cargobuild-123/vendor"`
	got := rewriteVendorConfig(snippet, "/work/app/vendor")
	if !strings.Contains(got, `directory = "/work/app/vendor"`) {
		t.Errorf("directory not rewritten to sandbox path:\n%s", got)
	}
	if strings.Contains(got, "/tmp/iceclimber-cargobuild-123") {
		t.Errorf("controller temp path leaked:\n%s", got)
	}
	if !strings.Contains(got, `replace-with = "vendored-sources"`) {
		t.Errorf("replacement wiring dropped:\n%s", got)
	}

	// Empty / shapeless snippet → synthesized canonical config.
	def := rewriteVendorConfig("", "/work/app/vendor")
	for _, want := range []string{"[source.crates-io]", `replace-with = "vendored-sources"`, `directory = "/work/app/vendor"`} {
		if !strings.Contains(def, want) {
			t.Errorf("default config missing %q:\n%s", want, def)
		}
	}
	// A snippet with no directory line also falls back to a usable config.
	noDir := rewriteVendorConfig("[source.crates-io]\nreplace-with = \"vendored-sources\"", "/work/app/vendor")
	if !strings.Contains(noDir, `directory = "/work/app/vendor"`) {
		t.Errorf("no-directory snippet should synthesize a directory:\n%s", noDir)
	}
}

func TestCargoBuildCmd(t *testing.T) {
	online := cargoBuildCmd("/r/bin/cargo", "aarch64", "glibc", nil, false)
	for _, want := range []string{"build", "--release", "--message-format=json"} {
		if !strings.Contains(online, want) {
			t.Errorf("online cmd missing %q: %s", want, online)
		}
	}
	if strings.Contains(online, "--offline") {
		t.Errorf("online cmd should not be --offline: %s", online)
	}
	offline := cargoBuildCmd("/r/bin/cargo", "aarch64", "glibc", []string{"--features", "x y"}, true)
	if !strings.Contains(offline, "--offline") {
		t.Errorf("offline cmd missing --offline: %s", offline)
	}
	if !strings.Contains(offline, "'x y'") { // extra args shell-quoted
		t.Errorf("extra arg not quoted: %s", offline)
	}
	// glibc: no bundled-linker override (the gnu target needs a system toolchain).
	if strings.Contains(online, "rust-lld") {
		t.Errorf("glibc cmd should not force rust-lld: %s", online)
	}
	// musl: the bundled rust-lld linker is injected (no system cc on the box) + its sysroot bin
	// dir is on PATH so rust-lld resolves.
	musl := cargoBuildCmd("/r/bin/cargo", "aarch64", "musl", nil, false)
	if !strings.Contains(musl, "-C linker=rust-lld") {
		t.Errorf("musl cmd should force rust-lld: %s", musl)
	}
	if !strings.Contains(musl, "/r/lib/rustlib/aarch64-unknown-linux-musl/bin") {
		t.Errorf("musl cmd should put the rust-lld sysroot bin on PATH: %s", musl)
	}
}

func TestLooksLikeProxyDown(t *testing.T) {
	if !looksLikeProxyDown("error: failed to connect to 127.0.0.1:8080: Connection refused") {
		t.Error("should detect proxy-down")
	}
	if looksLikeProxyDown("error[E0432]: unresolved import") {
		t.Error("a compile error is not proxy-down")
	}
}
