//go:build functional

package functional

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/probe"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

func TestMain(m *testing.M) {
	bin, cleanup, err := buildBinary()
	if err != nil {
		fmt.Fprintln(os.Stderr, "functional: build iceclimber:", err)
		os.Exit(1)
	}
	iceclimberBin = bin
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func buildBinary() (string, func(), error) {
	dir, err := os.MkdirTemp("", "iceclimber-bin")
	if err != nil {
		return "", nil, err
	}
	// Cross-compile the in-sandbox popo client so iceclimber embeds it (matches what
	// `make build` does) — otherwise bootstrap can't drop popo on the VM.
	for _, ga := range []string{"arm64", "amd64"} {
		pb := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w",
			"-o", filepath.Join("internal", "popobin", "bin", "popo-linux-"+ga), "./cmd/popo")
		pb.Dir = repoRoot()
		pb.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+ga)
		if out, err := pb.CombinedOutput(); err != nil {
			os.RemoveAll(dir)
			return "", nil, fmt.Errorf("build popo (%s): %v\n%s", ga, err, out)
		}
	}

	bin := filepath.Join(dir, "iceclimber")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = repoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("%v\n%s", err, out)
	}
	return bin, func() { os.RemoveAll(dir) }, nil
}

// TestProbe_AlpineSandbox is the core proof: the real binary, over real SSH,
// fingerprints a real Alpine (musl/BusyBox) box correctly.
func TestProbe_AlpineSandbox(t *testing.T) {
	sb := requireSandbox(t)
	// Point the existing-tree check at a guaranteed-fresh root so the false-case
	// assertion is deterministic regardless of what else has bootstrapped the box.
	cfg := writeConfigRoot(t, sb, "/tmp/iceclimber-probe-"+protocol.NewID())

	out := runIceclimber(t, "probe", "--json", "--config", cfg)

	var fp probe.Fingerprint
	if err := json.Unmarshal(out, &fp); err != nil {
		t.Fatalf("unmarshal probe json: %v\noutput: %s", err, out)
	}

	if fp.OS != "linux" {
		t.Errorf("OS = %q, want linux", fp.OS)
	}
	if fp.Arch != "aarch64" && fp.Arch != "x86_64" {
		t.Errorf("Arch = %q, want aarch64 or x86_64", fp.Arch)
	}
	if fp.Libc.Family != "musl" || !fp.Libc.HighConfidence {
		t.Errorf("Libc = %+v, want musl with high confidence", fp.Libc)
	}
	if root := fp.FirstViableRoot(); root == "" {
		t.Errorf("no writable install root found; roots = %+v", fp.Roots)
	}
	if fp.HasExistingTree {
		t.Error("HasExistingTree = true on a fresh sandbox")
	}
	if len(fp.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", fp.Warnings)
	}
}

// TestProbe_RejectsUnknownHostKey proves strict host-key verification end to
// end: an empty known_hosts means the host is unknown, so probe must fail
// rather than trust-on-first-use.
func TestProbe_RejectsUnknownHostKey(t *testing.T) {
	sb := requireSandbox(t)
	empty := filepath.Join(t.TempDir(), "empty_known_hosts")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	sb.KnownHosts = empty
	cfg := writeConfig(t, sb)

	out, err := exec.Command(iceclimberBin, "probe", "--config", cfg).CombinedOutput()
	if err == nil {
		t.Fatalf("probe succeeded with an unknown host key; want failure:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(string(out)), "key") {
		t.Errorf("error should mention the host key, got: %s", out)
	}
}

func TestConfigValidate_AgainstSandbox(t *testing.T) {
	sb := requireSandbox(t)
	cfg := writeConfig(t, sb)
	out := runIceclimber(t, "config", "validate", "--config", cfg)
	if !strings.Contains(string(out), "valid") {
		t.Errorf("config validate output = %q, want it to report valid", out)
	}
}
