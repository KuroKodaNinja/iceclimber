package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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

func TestBuildDialPlan_ExplicitWins(t *testing.T) {
	// Explicit cfg user/port/known_hosts override what ssh -G would resolve.
	fakeResolve(t, "hostname h\nport 22\nuser resolveduser\nuserknownhostsfile /resolved\n")
	pw := false
	p, err := buildDialPlan(context.Background(), DialConfig{
		Host: "sandbox", User: "explicit", Port: 2200, KnownHosts: "/explicit", UseSSHConfig: &pw,
	})
	if err != nil {
		t.Fatal(err)
	}
	// UseSSHConfig=false → literal dial, no resolution, no proxy.
	if p.host != "sandbox" || p.user != "explicit" || p.port != 2200 || p.knownHosts != "/explicit" {
		t.Errorf("literal plan = %+v", p)
	}
	if p.proxyArgv != nil {
		t.Errorf("UseSSHConfig=false must not proxy: %q", p.proxyArgv)
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
