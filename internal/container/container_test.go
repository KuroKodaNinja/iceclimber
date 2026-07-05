package container

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeRunner records every argv and returns canned stdout/err keyed by a substring of
// the joined command, so tests assert command assembly without a real podman.
type fakeRunner struct {
	calls    [][]string
	respond  map[string]string // substring -> stdout
	fail     map[string]bool   // substring -> return an error
	fallback string
}

func (f *fakeRunner) Run(_ context.Context, argv ...string) (string, error) {
	f.calls = append(f.calls, argv)
	joined := strings.Join(argv, " ")
	for k, v := range f.respond {
		if strings.Contains(joined, k) {
			if f.fail[k] {
				return "", fmt.Errorf("fake error for %q", k)
			}
			return v, nil
		}
	}
	for k := range f.fail {
		if strings.Contains(joined, k) {
			return "", fmt.Errorf("fake error for %q", k)
		}
	}
	return f.fallback, nil
}

func (f *fakeRunner) lastMatching(sub string) []string {
	for i := len(f.calls) - 1; i >= 0; i-- {
		if strings.Contains(strings.Join(f.calls[i], " "), sub) {
			return f.calls[i]
		}
	}
	return nil
}

func (f *fakeRunner) sawAny(sub string) bool { return f.lastMatching(sub) != nil }

func TestNew_DefaultsToRootfulSudoPodman(t *testing.T) {
	r := &fakeRunner{}
	c := New(r)
	c.Available(context.Background())
	got := r.calls[0]
	if len(got) < 2 || got[0] != "sudo" || got[1] != "podman" {
		t.Fatalf("default base should be [sudo podman], got %v", got)
	}
	// override
	r2 := &fakeRunner{}
	New(r2, "podman").Available(context.Background())
	if r2.calls[0][0] != "podman" {
		t.Fatalf("override base not honored: %v", r2.calls[0])
	}
}

func TestEnsureNetwork_CreatesInternalWhenAbsent(t *testing.T) {
	r := &fakeRunner{fail: map[string]bool{"network inspect": true}} // absent
	if err := New(r).EnsureNetwork(context.Background(), "airgap"); err != nil {
		t.Fatal(err)
	}
	create := r.lastMatching("network create")
	if create == nil {
		t.Fatal("expected a `network create` call")
	}
	if !contains(create, "--internal") || !contains(create, "airgap") {
		t.Fatalf("network create must be --internal for the air-gap: %v", create)
	}
}

func TestEnsureNetwork_SkipsWhenPresent(t *testing.T) {
	r := &fakeRunner{respond: map[string]string{"network inspect": "[{...}]"}} // present
	if err := New(r).EnsureNetwork(context.Background(), "airgap"); err != nil {
		t.Fatal(err)
	}
	if r.sawAny("network create") {
		t.Fatal("must not re-create an existing network")
	}
}

func TestRun_AssemblesSshdOnInternalNet(t *testing.T) {
	r := &fakeRunner{respond: map[string]string{"run -d": "containerid123"}}
	id, err := New(r).Run(context.Background(), RunSpec{
		Name: "icx-box", Image: "img:base", Network: "airgap", Runtime: "/usr/local/bin/runsc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "containerid123" {
		t.Fatalf("id = %q", id)
	}
	run := r.lastMatching("run -d")
	for _, want := range []string{"--name", "icx-box", "--network", "airgap", "--runtime", "/usr/local/bin/runsc", "/usr/sbin/sshd", "-D"} {
		if !contains(run, want) {
			t.Fatalf("run args missing %q: %v", want, run)
		}
	}
	// --runtime is a GLOBAL podman flag: it must come BEFORE the `run` subcommand.
	if idx(run, "--runtime") > idx(run, "run") {
		t.Fatalf("--runtime must precede `run` (global flag): %v", run)
	}
	// air-gap: must NOT publish a port to the host (egress/exposure minimal; reach via bridge IP).
	if strings.Contains(strings.Join(run, " "), "-p ") {
		t.Fatalf("sandbox container must not publish host ports: %v", run)
	}
}

func TestEnsureRunsc_InstallsWhenAbsent(t *testing.T) {
	// runsc --version fails until installed; the install command must download to RunscPath.
	calls := 0
	r := &installFakeRunner{onVersion: func() (string, error) {
		calls++
		if calls == 1 {
			return "", fmt.Errorf("not found")
		}
		return "runsc version release-x", nil
	}}
	if err := New(r).EnsureRunsc(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.sawInstall {
		t.Fatal("expected a runsc download/install command")
	}
	// present → no install
	r2 := &installFakeRunner{onVersion: func() (string, error) { return "runsc version release-y", nil }}
	if err := New(r2).EnsureRunsc(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r2.sawInstall {
		t.Fatal("must not reinstall runsc when already present")
	}
}

// TestRunscInstallScript_PinsAndVerifies is the supply-chain guard: the runsc install must
// pull a PINNED release (never the mutable "latest") and verify the download against the
// source-embedded SHA-512 (fail-closed) before installing it as a root-run container runtime.
func TestRunscInstallScript_PinsAndVerifies(t *testing.T) {
	s := runscInstallScript()
	if strings.Contains(s, "release/latest") {
		t.Error("runsc must be pinned to a dated release, not mutable 'latest'")
	}
	if !strings.Contains(s, "release/"+runscRelease) {
		t.Errorf("script should pin release %s: %q", runscRelease, s)
	}
	if !strings.Contains(s, "sha512sum -c") {
		t.Error("script must verify the download with sha512sum -c (fail-closed)")
	}
	for arch, want := range runscSHA512 {
		if !strings.Contains(s, want) {
			t.Errorf("script must embed the %s SHA-512 to verify against", arch)
		}
	}
	// The verification must precede installation, or a bad binary is placed before the check.
	if idxOf(s, "sha512sum -c") > idxOf(s, "install -m 0755") {
		t.Error("checksum verification must come before install")
	}
}

func idxOf(hay, needle string) int { return strings.Index(hay, needle) }

// installFakeRunner distinguishes the `runsc --version` probe from the install command.
type installFakeRunner struct {
	onVersion  func() (string, error)
	sawInstall bool
}

func (f *installFakeRunner) Run(_ context.Context, argv ...string) (string, error) {
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "runsc --version") || (strings.HasSuffix(argv[0], "runsc") && contains(argv, "--version")) {
		return f.onVersion()
	}
	if strings.Contains(joined, "gvisor/releases") {
		f.sawInstall = true
		return "", nil
	}
	return "", nil
}

func idx(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return len(ss)
}

func TestRun_NoRuntimeFlagWhenDefault(t *testing.T) {
	r := &fakeRunner{respond: map[string]string{"run -d": "id"}}
	if _, err := New(r).Run(context.Background(), RunSpec{Name: "b", Network: "n"}); err != nil {
		t.Fatal(err)
	}
	if contains(r.lastMatching("run -d"), "--runtime") {
		t.Fatal("no --runtime flag expected when RunSpec.Runtime is empty")
	}
	if !contains(r.lastMatching("run -d"), DefaultImage) {
		t.Fatalf("expected DefaultImage, got %v", r.lastMatching("run -d"))
	}
}

func TestKeyscan_UsesPortAndRetriesFormat(t *testing.T) {
	r := &fakeRunner{respond: map[string]string{"ssh-keyscan": "10.0.0.2 ssh-ed25519 AAAA"}}
	out, err := New(r).Keyscan(context.Background(), "10.0.0.2", 22)
	if err != nil || !strings.Contains(out, "ssh-ed25519") {
		t.Fatalf("Keyscan = %q, %v", out, err)
	}
	call := r.lastMatching("ssh-keyscan")
	if !contains(call, "-p") || !contains(call, "22") || !contains(call, "10.0.0.2") {
		t.Fatalf("keyscan should target -p 22 <ip>: %v", call)
	}
}

func TestIP_EmptyIsError(t *testing.T) {
	r := &fakeRunner{respond: map[string]string{"inspect": "   "}}
	if _, err := New(r).IP(context.Background(), "b"); err == nil {
		t.Fatal("empty IP must be an error (container not ready)")
	}
}

func TestStart_AssemblesPodmanStart(t *testing.T) {
	r := &fakeRunner{}
	if err := New(r).Start(context.Background(), "icx-box"); err != nil {
		t.Fatal(err)
	}
	call := r.lastMatching("start")
	if call == nil || !contains(call, "icx-box") {
		t.Fatalf("expected `podman start icx-box`, got %v", r.calls)
	}
}

func TestEnsureImage_BuildsWhenAbsent_SkipsWhenPresent(t *testing.T) {
	// absent: image exists fails -> build+commit
	r := &fakeRunner{fail: map[string]bool{"image exists": true}}
	if err := New(r).EnsureImage(context.Background(), "img:base", "ubuntu:24.04"); err != nil {
		t.Fatal(err)
	}
	if !r.sawAny("commit") || !r.sawAny("run --name iceclimber-build") {
		t.Fatalf("expected a provision run + commit; calls=%v", r.calls)
	}
	// the provision must run on the base image (default net = internet)
	if !contains(r.lastMatching("run --name iceclimber-build"), "ubuntu:24.04") {
		t.Fatal("provision should run against the base image")
	}

	// present: no build
	r2 := &fakeRunner{respond: map[string]string{"image exists": "ok"}}
	if err := New(r2).EnsureImage(context.Background(), "img:base", "ubuntu:24.04"); err != nil {
		t.Fatal(err)
	}
	if r2.sawAny("commit") {
		t.Fatal("must not rebuild an existing image")
	}
}

func TestAuthorizeKey_QuotesAndTargetsUser(t *testing.T) {
	r := &fakeRunner{}
	key := "ssh-ed25519 AAAAkey comment"
	if err := New(r).AuthorizeKey(context.Background(), "box", "agent", key); err != nil {
		t.Fatal(err)
	}
	call := strings.Join(r.lastMatching("exec box"), " ")
	if !strings.Contains(call, "/home/agent/.ssh") {
		t.Fatalf("non-root user should target its home: %s", call)
	}
	if !strings.Contains(call, key) {
		t.Fatalf("pubkey should be embedded: %s", call)
	}
}

func TestShSingleQuote(t *testing.T) {
	if got := shSingleQuote("a'b"); got != `'a'\''b'` {
		t.Fatalf("shSingleQuote(a'b) = %s", got)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
