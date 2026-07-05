//go:build functional

package functional

import (
	"os/exec"
	"runtime"
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
	id := protocol.NewID() // ULID: 10 timestamp chars + 16 random — take the RANDOM tail
	name := "icxfn" + strings.ToLower(id[len(id)-8:])
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
// create (Linux host — native on Linux, the shared Lima VM on macOS — + podman/netavark + an
// internal no-NAT bridge + a bootstrapped container) → list sees it → the air-gap is
// HOST-enforced and guest-root-proof (even a --privileged container-root that re-adds a
// default route cannot egress) → a STOPPED container self-heals on the next dial (decision
// #112: restarted, endpoint re-resolved, status reports the healed state) → rm removes the
// container but leaves the host. Skips without a usable container host.
// This is the dogfood: our own tests use the new air-gap method.
func TestContainerSandboxLifecycle(t *testing.T) {
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("podman"); err != nil {
			t.Skip("podman not found; install rootful podman to run the container sandbox lifecycle test")
		}
		if exec.Command("sudo", "-n", "podman", "version").Run() != nil {
			t.Skip("sudo podman needs a password; grant NOPASSWD for podman to run this test")
		}
	} else if _, err := exec.LookPath("limactl"); err != nil {
		t.Skip("limactl not found; install Lima to run the container sandbox lifecycle test")
	}
	id := protocol.NewID() // ULID: 10 timestamp chars + 16 random — take the RANDOM tail
	name := "icxc" + strings.ToLower(id[len(id)-8:])
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
	esc := podmanOnHost(t, "exec", "--privileged", cname, "sh", "-c",
		"ip route add default via 10.89.0.1 2>/dev/null; "+
			"timeout 5 sh -c 'exec 3<>/dev/tcp/1.1.1.1/443' 2>/dev/null && echo LEAK || echo blocked")
	if !strings.Contains(esc, "blocked") {
		t.Errorf("container-root defeated the host-enforced air-gap: %q", strings.TrimSpace(esc))
	}

	// A stopped container self-heals on the next dial (decision #112): `sandbox status`
	// restarts it, re-resolves the bridge IP (assigned anew on start), rewrites the
	// recorded endpoint, and reports the HEALED state — Running, with the SSH summary
	// reachable — not the pre-dial "Exited".
	podmanOnHost(t, "stop", "-t", "5", cname)
	st := string(runIceclimber(t, "sandbox", "status", name))
	if !strings.Contains(st, "Running") || strings.Contains(st, "Exited") {
		t.Errorf("status after a stop should report the self-healed container as Running:\n%s", st)
	}
	if !strings.Contains(st, "queue") {
		t.Errorf("status after self-heal should reach the sandbox over SSH (summary line):\n%s", st)
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

// podmanOnHost runs `sudo podman <args...>` on the Linux host that owns the sandbox
// containers — directly on native Linux, via `limactl shell` into the shared host VM
// (iceclimber-host) on macOS — returning combined output. Used to adversarially probe and
// manipulate a sandbox container from the host's vantage.
func podmanOnHost(t *testing.T, args ...string) string {
	t.Helper()
	full := append([]string{"sudo", "podman"}, args...)
	if runtime.GOOS != "linux" {
		full = append([]string{"limactl", "shell", "iceclimber-host", "--"}, full...)
	}
	out, _ := exec.Command(full[0], full[1:]...).CombinedOutput()
	return string(out)
}
