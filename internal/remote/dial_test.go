package remote

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeResolve swaps runSSHG to return fixed `ssh -G` output (and records args),
// restoring it on cleanup. Skips the test if no ssh client exists (resolveSSHConfig
// requires the binary to be present before it runs the resolver).
func fakeResolve(t *testing.T, output string) *[]string {
	t.Helper()
	if _, err := sshBinary(); err != nil {
		t.Skip("no ssh client; buildDialPlan resolution path needs sshBinary()")
	}
	orig := runSSHG
	t.Cleanup(func() { runSSHG = orig })
	var got []string
	runSSHG = func(_ context.Context, _ string, args []string) ([]byte, error) {
		got = args
		return []byte(output), nil
	}
	return &got
}

func TestBuildDialPlan_ResolvesAndProxies(t *testing.T) {
	fakeResolve(t, "hostname 10.0.2.15\nport 2222\nuser agent\nproxyjump bastion\nuserknownhostsfile /custom/known_hosts\n")
	p, err := buildDialPlan(context.Background(), DialConfig{Host: "sandbox"})
	if err != nil {
		t.Fatal(err)
	}
	if p.host != "10.0.2.15" || p.port != 2222 || p.user != "agent" {
		t.Errorf("plan host/port/user = %q/%d/%q", p.host, p.port, p.user)
	}
	if p.knownHosts != "/custom/known_hosts" {
		t.Errorf("knownHosts = %q, want resolved UserKnownHostsFile", p.knownHosts)
	}
	if len(p.proxyArgv) == 0 || p.proxyArgv[len(p.proxyArgv)-1] != "bastion" {
		t.Errorf("expected a proxy argv ending in the jump host, got %q", p.proxyArgv)
	}
}

func TestBuildDialPlan_ResolutionDisabled(t *testing.T) {
	// UseSSHConfig=false → literal dial, no `ssh -G`, no proxy. (This used to be
	// misnamed "ExplicitWins" — it tests the disabled path, not precedence.)
	pw := false
	p, err := buildDialPlan(context.Background(), DialConfig{
		Host: "sandbox", User: "explicit", Port: 2200, KnownHosts: "/explicit", UseSSHConfig: &pw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.host != "sandbox" || p.user != "explicit" || p.port != 2200 || p.knownHosts != "/explicit" {
		t.Errorf("literal plan = %+v", p)
	}
	if p.proxyArgv != nil {
		t.Errorf("UseSSHConfig=false must not proxy: %q", p.proxyArgv)
	}
}

// TestBuildDialPlan_ExplicitWinsOverResolved is the real precedence test: with
// resolution ON, explicit cfg user/port/known_hosts must override what `ssh -G`
// returns — while the resolved ProxyJump still applies (it has no explicit field).
func TestBuildDialPlan_ExplicitWinsOverResolved(t *testing.T) {
	fakeResolve(t, "hostname 10.0.0.9\nport 22\nuser resolveduser\nuserknownhostsfile /resolved_kh\nproxyjump bastion\n")
	p, err := buildDialPlan(context.Background(), DialConfig{
		Host: "sandbox", User: "explicit", Port: 2200, KnownHosts: "/explicit_kh",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.user != "explicit" || p.port != 2200 || p.knownHosts != "/explicit_kh" {
		t.Errorf("explicit user/port/known_hosts must win; got user=%q port=%d kh=%q", p.user, p.port, p.knownHosts)
	}
	if p.host != "10.0.0.9" { // host always becomes the resolved HostName
		t.Errorf("host = %q, want resolved 10.0.0.9", p.host)
	}
	if len(p.proxyArgv) == 0 || p.proxyArgv[len(p.proxyArgv)-1] != "bastion" {
		t.Errorf("resolved ProxyJump should still apply: %q", p.proxyArgv)
	}
}

// TestBuildDialPlan_IdentityOrder: the explicit identity file is tried first, then
// ssh -G's IdentityFiles, deduped — so the operator's key wins.
func TestBuildDialPlan_IdentityOrder(t *testing.T) {
	fakeResolve(t, "hostname h\nport 22\nidentityfile /resolved/a\nidentityfile /resolved/b\n")
	p, err := buildDialPlan(context.Background(), DialConfig{Host: "sandbox", IdentityFile: "/explicit/key"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/explicit/key", "/resolved/a", "/resolved/b"}
	if len(p.identityFiles) != 3 || p.identityFiles[0] != want[0] || p.identityFiles[1] != want[1] || p.identityFiles[2] != want[2] {
		t.Errorf("identityFiles = %q, want explicit-then-resolved %q", p.identityFiles, want)
	}
}

func TestResolveTarget(t *testing.T) {
	// ResolveTarget reports the RESOLVED host (not the alias) so host-key trust is
	// keyed on the real host — the property that lets trust work behind a bastion.
	fakeResolve(t, "hostname 192.168.1.50\nport 2222\nuserknownhostsfile /kh\n")
	host, port, kh, err := ResolveTarget(context.Background(), DialConfig{Host: "alias"})
	if err != nil {
		t.Fatal(err)
	}
	if host != "192.168.1.50" || port != 2222 || kh != "/kh" {
		t.Errorf("ResolveTarget = %q/%d/%q, want the resolved values", host, port, kh)
	}
}

func TestProxyDetail(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	pc, err := newProxyConn(context.Background(), helperArgv("fail"), "10.0.0.1:22")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(pc)
	_ = pc.Close()
	if got := proxyDetail(pc); !strings.Contains(got, "permission denied") {
		t.Errorf("proxyDetail(proxyConn) = %q, want the bastion stderr tail", got)
	}
	// A plain (non-proxy) conn enriches nothing.
	if got := proxyDetail(plainConn{}); got != "" {
		t.Errorf("proxyDetail(plain) = %q, want empty", got)
	}
}

func TestBuildDialPlan_ExpandsResolvedKnownHosts(t *testing.T) {
	fakeResolve(t, "hostname h\nport 22\nuserknownhostsfile ~/.ssh/corp_known_hosts\n")
	p, err := buildDialPlan(context.Background(), DialConfig{Host: "sandbox"})
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".ssh", "corp_known_hosts")
	if p.knownHosts != want {
		t.Errorf("resolved known_hosts = %q, want tilde-expanded %q", p.knownHosts, want)
	}
}

// TestBuildDialPlan_NoSSHClient: when the ssh binary is absent, an unset
// UseSSHConfig degrades to a literal direct dial (no error), but an explicit
// use_ssh_config:true fails loud rather than silently losing a ProxyJump.
func TestBuildDialPlan_NoSSHClient(t *testing.T) {
	t.Setenv("PATH", "") // make exec.LookPath("ssh") fail → errSSHUnavailable

	p, err := buildDialPlan(context.Background(), DialConfig{Host: "sandbox", Port: 2200})
	if err != nil {
		t.Fatalf("default (nil UseSSHConfig) should fall back silently: %v", err)
	}
	if p.host != "sandbox" || p.port != 2200 || p.proxyArgv != nil {
		t.Errorf("fallback plan = %+v, want literal direct dial", p)
	}

	yes := true
	if _, err := buildDialPlan(context.Background(), DialConfig{Host: "sandbox", UseSSHConfig: &yes}); err == nil {
		t.Error("explicit use_ssh_config:true with no ssh client must fail, not silently direct-dial")
	}
}

func TestBuildDialPlan_PortDefault(t *testing.T) {
	off := false
	p, err := buildDialPlan(context.Background(), DialConfig{Host: "h", UseSSHConfig: &off})
	if err != nil {
		t.Fatal(err)
	}
	if p.port != 22 || len(p.proxyArgv) != 0 {
		t.Errorf("default plan = %+v, want port 22, no proxy", p)
	}
}

// plainConn is a no-op net.Conn for proxyDetail's non-proxy branch.
type plainConn struct{}

func (plainConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (plainConn) Write(b []byte) (int, error)      { return len(b), nil }
func (plainConn) Close() error                     { return nil }
func (plainConn) LocalAddr() net.Addr              { return nil }
func (plainConn) RemoteAddr() net.Addr             { return nil }
func (plainConn) SetDeadline(t time.Time) error    { return nil }
func (plainConn) SetReadDeadline(time.Time) error  { return nil }
func (plainConn) SetWriteDeadline(time.Time) error { return nil }
