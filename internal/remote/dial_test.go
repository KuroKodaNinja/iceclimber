package remote

import (
	"context"
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
