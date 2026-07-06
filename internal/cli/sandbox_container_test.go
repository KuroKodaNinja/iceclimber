package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/container"
	"github.com/KuroKodaNinja/iceclimber/internal/xdg"
)

// TestContainerSSHConfigHasIP: the restart-refresh only rewrites the ssh_config when the
// container's bridge IP actually changed (a host reboot can hand it a new one).
func TestContainerSSHConfigHasIP(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ssh_config")
	cfg := "Host icx-jump-box\n  HostName 127.0.0.1\n  Port 52724\n\n" +
		"Host box\n  HostName 10.89.0.7\n  Port 22\n  User agent\n"
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if !containerSSHConfigHasIP(p, "10.89.0.7") {
		t.Fatal("should recognize the current container IP (no rewrite needed)")
	}
	if containerSSHConfigHasIP(p, "10.89.0.9") {
		t.Fatal("a changed container IP must NOT match — it triggers a rewrite")
	}
	if containerSSHConfigHasIP(filepath.Join(dir, "missing"), "10.89.0.7") {
		t.Fatal("a missing ssh_config should report not-current (force a rewrite)")
	}
}

// fakePodmanHost implements container.Runner with canned stdout keyed by a substring of
// the joined argv (the same pattern as the container package's fakeRunner), so the refresh
// path can be driven without a real podman.
type fakePodmanHost struct {
	calls   [][]string
	respond map[string]string
	fail    map[string]bool
}

func (f *fakePodmanHost) Run(_ context.Context, argv ...string) (string, error) {
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
	return "", nil
}

func (f *fakePodmanHost) saw(sub string) bool {
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), sub) {
			return true
		}
	}
	return false
}

// containerSandboxFixture registers a container sandbox whose generated ssh_config and
// known_hosts record staleIP, returning its config. The key material in known_hosts is
// the pinned trust the refresh must preserve.
func containerSandboxFixture(t *testing.T, staleIP, pinnedKey string) *config.Config {
	t.Helper()
	isolateRegistry(t)
	sbDir := xdg.SandboxDir("boxy")
	if err := os.MkdirAll(sbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(sbDir, "known_hosts")
	if err := os.WriteFile(known, []byte(staleIP+" ssh-ed25519 "+pinnedKey+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(sbDir, "id_ed25519")
	if _, err := writeContainerSSHConfig(sbDir, "boxy", staleIP, identity, known, "native", ""); err != nil {
		t.Fatal(err)
	}
	useCfg := true
	return &config.Config{
		SandboxID: "boxy",
		SSH: config.SSH{
			Host: "boxy", Port: 22, User: "agent",
			IdentityFile: identity, KnownHosts: known,
			UseSSHConfig: &useCfg, SSHConfigFile: filepath.Join(sbDir, "ssh_config"),
		},
		Provider: &config.Provider{
			Type: "container", Instance: "iceclimber-boxy",
			Host: "native", Network: "iceclimber-airgap",
		},
	}
}

// TestRefreshContainerSSH_StartsStoppedAndRepointsEndpoint: the documented recovery
// contract — a stopped container is started, and because the bridge IP is assigned anew
// on start, the generated ssh_config (HostName) and known_hosts are re-pointed at the
// re-resolved address while the PINNED KEY is preserved (never re-trusted from the wire).
func TestRefreshContainerSSH_StartsStoppedAndRepointsEndpoint(t *testing.T) {
	cfg := containerSandboxFixture(t, "10.89.0.2", "AAAApinned")
	host := &fakePodmanHost{respond: map[string]string{
		".State.Status": "exited",
		"IPAddress":     "10.89.0.9", // the new address after start
		// A scan that presents DIFFERENT key material — it must be used only as a
		// readiness gate, never written into known_hosts.
		"ssh-keyscan": "10.89.0.9 ssh-ed25519 AAAAattacker",
	}}
	if err := refreshContainerSSHWith(context.Background(), cfg, container.New(host)); err != nil {
		t.Fatal(err)
	}

	if !host.saw("start iceclimber-boxy") {
		t.Fatalf("stopped container must be started; calls=%v", host.calls)
	}
	if !host.saw("ssh-keyscan") {
		t.Fatal("a just-started sshd must be readiness-gated (keyscan)")
	}

	sshCfg, err := os.ReadFile(cfg.SSH.SSHConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sshCfg), "HostName 10.89.0.9") {
		t.Fatalf("ssh_config must dial the re-resolved IP:\n%s", sshCfg)
	}
	if strings.Contains(string(sshCfg), "10.89.0.2") {
		t.Fatalf("stale IP must not survive in ssh_config:\n%s", sshCfg)
	}

	known, err := os.ReadFile(cfg.SSH.KnownHosts)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(known))
	if got != "10.89.0.9 ssh-ed25519 AAAApinned" {
		t.Fatalf("known_hosts must carry the new IP with the ORIGINAL pinned key, got %q", got)
	}
}

// TestRefreshContainerSSH_RunningContainerIsNotRestarted: a running container whose IP
// moved is left alone — only the recorded endpoint is refreshed.
func TestRefreshContainerSSH_RunningContainerIsNotRestarted(t *testing.T) {
	cfg := containerSandboxFixture(t, "10.89.0.2", "AAAApinned")
	host := &fakePodmanHost{respond: map[string]string{
		".State.Status": "running",
		"IPAddress":     "10.89.0.7",
	}}
	if err := refreshContainerSSHWith(context.Background(), cfg, container.New(host)); err != nil {
		t.Fatal(err)
	}
	if host.saw("start iceclimber-boxy") {
		t.Fatalf("running container must not be restarted; calls=%v", host.calls)
	}
	sshCfg, _ := os.ReadFile(cfg.SSH.SSHConfigFile)
	if !strings.Contains(string(sshCfg), "HostName 10.89.0.7") {
		t.Fatalf("ssh_config must be refreshed to the current IP:\n%s", sshCfg)
	}
}

// TestRefreshContainerSSH_StoppedWithSameIPGatesReadiness: when the restart hands back the
// SAME IP, no rewrite happens — but the just-started sshd is still readiness-gated before
// the caller dials (a refused dial right after start has no retry).
func TestRefreshContainerSSH_StoppedWithSameIPGatesReadiness(t *testing.T) {
	cfg := containerSandboxFixture(t, "10.89.0.2", "AAAApinned")
	host := &fakePodmanHost{respond: map[string]string{
		".State.Status": "exited",
		"IPAddress":     "10.89.0.2", // unchanged across the restart
		"ssh-keyscan":   "10.89.0.2 ssh-ed25519 AAAApinned",
	}}
	before, _ := os.ReadFile(cfg.SSH.KnownHosts)
	if err := refreshContainerSSHWith(context.Background(), cfg, container.New(host)); err != nil {
		t.Fatal(err)
	}
	if !host.saw("start iceclimber-boxy") || !host.saw("ssh-keyscan") {
		t.Fatalf("stopped container must be started and readiness-gated; calls=%v", host.calls)
	}
	after, _ := os.ReadFile(cfg.SSH.KnownHosts)
	if string(before) != string(after) {
		t.Fatalf("an unchanged IP must not rewrite known_hosts:\nbefore %q\nafter  %q", before, after)
	}
}

// TestRefreshContainerSSH_AbsentContainerErrors: a removed container cannot be refreshed —
// the operator must recreate; the error says so instead of half-writing files.
func TestRefreshContainerSSH_AbsentContainerErrors(t *testing.T) {
	cfg := containerSandboxFixture(t, "10.89.0.2", "AAAApinned")
	host := &fakePodmanHost{fail: map[string]bool{".State.Status": true}}
	err := refreshContainerSSHWith(context.Background(), cfg, container.New(host))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want a not-found error, got %v", err)
	}
	// The stale files must be untouched (nothing to re-point at).
	known, _ := os.ReadFile(cfg.SSH.KnownHosts)
	if !strings.Contains(string(known), "10.89.0.2") {
		t.Fatalf("known_hosts must be untouched on failure, got %q", known)
	}
}

// TestRewriteKnownHostsAddr_PreservesKeysAndComments: the helper's contract — every entry
// is re-addressed, key material is byte-identical, comment lines pass through.
func TestRewriteKnownHostsAddr_PreservesKeysAndComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	body := "# scanned at create\n10.0.0.1 ssh-ed25519 KEY1\n10.0.0.1 ssh-rsa KEY2 trailing comment\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rewriteKnownHostsAddr(path, "10.9.9.9"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := "# scanned at create\n10.9.9.9 ssh-ed25519 KEY1\n10.9.9.9 ssh-rsa KEY2 trailing comment\n"
	if string(got) != want {
		t.Fatalf("rewrite mismatch:\ngot  %q\nwant %q", got, want)
	}
}

// TestAtomicWriteFile round-trips content + perm and never leaves a .tmp sibling behind — the
// property that keeps a concurrent ssh dial from reading a half-written ssh_config/known_hosts.
func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ssh_config")
	if err := atomicWriteFile(path, []byte("Host x\n  HostName 10.0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(got), "10.0.0.1") {
		t.Fatalf("content round-trip failed: %q err=%v", got, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v, want 0600", fi.Mode().Perm())
	}
	// Overwrite (the rewrite-on-IP-change path) still lands atomically with no temp residue.
	if err := atomicWriteFile(path, []byte("Host x\n  HostName 10.0.0.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if len(ents) != 1 {
		t.Errorf("expected exactly ssh_config in dir, got %d entries", len(ents))
	}
}
