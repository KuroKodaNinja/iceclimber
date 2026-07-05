//go:build functional

package functional

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestSandboxLifecycle exercises `iceclimber sandbox` end to end against a REAL Lima VM: create
// (provision + register + air-gap + bootstrap) → list/status see it → the workload-external
// air-gap actually holds (direct egress blocked, the agent user has no sudo to flush it) → rm
// destroys the VM and the registry entry. Skips cleanly without limactl. This is the honest
// validation of §18 — the value is the VM lifecycle, which unit tests can't cover.
func TestSandboxLifecycle(t *testing.T) {
	if _, err := exec.LookPath("limactl"); err != nil {
		t.Skip("limactl not found; install Lima to run the sandbox lifecycle test")
	}
	name := "icxfn" + strings.ToLower(protocol.NewID())[:8]
	instance := "iceclimber-" + name

	// Always tear the VM + registry down, even on a mid-test failure (best-effort).
	t.Cleanup(func() { _ = exec.Command(iceclimberBin, "sandbox", "rm", name, "--force").Run() })

	out := string(runIceclimber(t, "sandbox", "create", name))
	for _, want := range []string{"air-gap applied", "bootstrapped", "ready"} {
		if !strings.Contains(out, want) {
			t.Errorf("create output missing %q:\n%s", want, out)
		}
	}

	// list shows it as a managed, running sandbox.
	if l := string(runIceclimber(t, "sandbox", "list")); !strings.Contains(l, name) || !strings.Contains(l, "lima:ubuntu") {
		t.Errorf("sandbox list did not show the created box:\n%s", l)
	}

	// The workload-external air-gap holds: a direct fetch to the internet is blocked, but DNS
	// (allowed) still resolves — proving the lockdown is selective, not a broken network.
	if s := limaShOn(t, instance, "curl -sS -m 6 https://example.com >/dev/null 2>&1 && echo REACHED || echo blocked"); !strings.Contains(s, "blocked") {
		t.Errorf("air-gap did not block direct egress: %q", strings.TrimSpace(s))
	}
	if s := limaShOn(t, instance, "nslookup example.com >/dev/null 2>&1 && echo dnsok || echo dnsblocked"); !strings.Contains(s, "dnsok") {
		t.Errorf("DNS should still resolve under the air-gap: %q", strings.TrimSpace(s))
	}
	// The agent's LLM API IS reachable — a Claude agent in the box must reach its model directly
	// (Popo bridges everything else). A TCP connect to the Anthropic API host on 443 must succeed
	// even though general egress is blocked. curl returning ANY HTTP status (e.g. 401/404) proves
	// the connection + TLS completed; a firewall block yields "Connection timed out".
	if s := limaShOn(t, instance, "curl -sS -o /dev/null -w '%{http_code}' -m 12 https://api.anthropic.com/v1/messages 2>/dev/null || echo BLOCKED"); strings.Contains(s, "BLOCKED") || strings.TrimSpace(s) == "000" {
		t.Errorf("the agent's LLM API (api.anthropic.com:443) must be reachable through the air-gap, got %q", strings.TrimSpace(s))
	}
	// The nana workload user has no sudo — so it can't flush the firewall (the seam's v1 bar).
	if s := limaShOn(t, instance, "sudo -u agent sudo -n true 2>/dev/null && echo HASSUDO || echo nosudo"); !strings.Contains(s, "nosudo") {
		t.Errorf("the agent user should not have sudo: %q", strings.TrimSpace(s))
	}

	// rm destroys the VM and forgets the registry entry.
	rmOut := string(runIceclimber(t, "sandbox", "rm", name, "--force"))
	if !strings.Contains(rmOut, "deleted lima instance") || !strings.Contains(rmOut, "removed sandbox") {
		t.Errorf("rm output unexpected:\n%s", rmOut)
	}
	if inst, err := exec.Command("limactl", "list", "--quiet", instance).Output(); err == nil && strings.Contains(string(inst), instance) {
		t.Errorf("lima instance %q still present after rm", instance)
	}
	if l := string(runIceclimber(t, "sandbox", "list")); strings.Contains(l, name) {
		t.Errorf("sandbox %q still in the registry after rm:\n%s", name, l)
	}
}

// TestContainerSandboxLifecycle exercises the CONTAINER backend (decision #111) end to end:
// create (shared Linux host VM + podman/netavark + an internal no-NAT bridge + a bootstrapped
// container, dialed over ProxyJump) → list sees it → the air-gap is HOST-enforced and
// guest-root-proof (even a --privileged container-root that re-adds a default route cannot
// egress) → rm removes the container but leaves the shared host VM. Skips without limactl.
// This is the dogfood: our own tests use the new air-gap method.
func TestContainerSandboxLifecycle(t *testing.T) {
	if _, err := exec.LookPath("limactl"); err != nil {
		t.Skip("limactl not found; install Lima to run the container sandbox lifecycle test")
	}
	name := "icxc" + strings.ToLower(protocol.NewID())[:8]
	cname := "iceclimber-" + name // the container name (and the internal-net gateway is 10.89.0.1)
	t.Cleanup(func() { _ = exec.Command(iceclimberBin, "sandbox", "rm", name, "--force").Run() })

	out := string(runIceclimber(t, "sandbox", "create", name, "--provider", "container"))
	for _, want := range []string{"internal no-NAT bridge", "bootstrapped", "ready"} {
		if !strings.Contains(out, want) {
			t.Errorf("container create output missing %q:\n%s", want, out)
		}
	}
	if l := string(runIceclimber(t, "sandbox", "list")); !strings.Contains(l, name) || !strings.Contains(l, "container:ubuntu") {
		t.Errorf("sandbox list did not show the container box:\n%s", l)
	}

	// The air-gap is enforced OUTSIDE the container (host netfilter on the internal no-NAT
	// bridge): even a privileged container-root that re-adds a default route cannot reach the
	// internet. This is the guest-root-proof property Lima's non-removable NIC can't give.
	esc := podmanExecOnHost(t, cname, "sh", "-c",
		"ip route add default via 10.89.0.1 2>/dev/null; "+
			"timeout 5 sh -c 'exec 3<>/dev/tcp/1.1.1.1/443' 2>/dev/null && echo LEAK || echo blocked")
	if !strings.Contains(esc, "blocked") {
		t.Errorf("container-root defeated the host-enforced air-gap: %q", strings.TrimSpace(esc))
	}

	// rm removes the container but leaves the shared host VM (it may back other sandboxes).
	rmOut := string(runIceclimber(t, "sandbox", "rm", name, "--force"))
	if !strings.Contains(rmOut, "removed container") || !strings.Contains(rmOut, "shared host VM left running") {
		t.Errorf("container rm output unexpected:\n%s", rmOut)
	}
	if l := string(runIceclimber(t, "sandbox", "list")); strings.Contains(l, name) {
		t.Errorf("container sandbox %q still in the registry after rm:\n%s", name, l)
	}
}

// podmanExecOnHost runs `sudo podman exec --privileged <args...>` inside the shared container
// host VM (iceclimber-host), returning combined output. Used to adversarially probe a sandbox
// container from the host's vantage.
func podmanExecOnHost(t *testing.T, args ...string) string {
	t.Helper()
	full := append([]string{"shell", "iceclimber-host", "--", "sudo", "podman", "exec", "--privileged"}, args...)
	out, _ := exec.Command("limactl", full...).CombinedOutput()
	return string(out)
}
