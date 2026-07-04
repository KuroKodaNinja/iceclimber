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
