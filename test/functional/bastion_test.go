//go:build functional

package functional

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBastionProxyJump exercises the whole jumpbox path end to end — `ssh -G`
// resolution from a pinned config, the ProxyJump→`ssh -W` synthesis, the
// ProxyCommand subprocess net.Conn, and the x/crypto handshake over it — using a
// SELF-JUMP: the controller reaches the sandbox THROUGH the sandbox acting as its
// own bastion (jumphost = the VM's forwarded port; target = 127.0.0.1:22, the VM's
// own sshd reached from inside the VM). No second VM is needed; the mechanism is
// identical to a real bastion topology.
//
// It proves the two things that must work behind a jump: `iceclimber trust`
// fetches the TARGET's host key through the jump, and `iceclimber probe` connects
// through the jump. Everything is hermetic — a pinned ssh_config_file with
// StrictHostKeyChecking disabled for the jump hop, so the dev/CI machine's real
// ~/.ssh/config is never consulted.
func TestBastionProxyJump(t *testing.T) {
	sb := requireSandbox(t)
	dir := t.TempDir()

	// Pinned ssh config: the target jumps through "jumphost" (the VM's forwarded
	// ssh port) to the VM's own internal sshd (127.0.0.1:22).
	sshCfg := filepath.Join(dir, "ssh_config")
	cfgBody := fmt.Sprintf(`Host sandbox-via-jump
  HostName 127.0.0.1
  Port 22
  User %[1]s
  IdentityFile %[2]s
  ProxyJump jumphost
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null

Host jumphost
  HostName %[3]s
  Port %[4]d
  User %[1]s
  IdentityFile %[2]s
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
`, sb.User, sb.IdentityFile, sb.Host, sb.Port)
	if err := os.WriteFile(sshCfg, []byte(cfgBody), 0o600); err != nil {
		t.Fatal(err)
	}

	// iceclimber.yaml names only the alias + the pinned config — host/port/user/
	// identity all come from `ssh -G`. known_hosts is a fresh file trust populates.
	kh := filepath.Join(dir, "known_hosts")
	cfg := filepath.Join(dir, "iceclimber.yaml")
	yaml := fmt.Sprintf(`sandbox_id: %s
ssh:
  host: sandbox-via-jump
  ssh_config_file: %s
  use_ssh_config: true
  known_hosts: %s
`, sandboxName, sshCfg, kh)
	if err := os.WriteFile(cfg, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	// trust through the jump: FetchHostKey spawns `ssh -F <pinned> -W 127.0.0.1:22
	// jumphost` and captures the TARGET key, recording it under the resolved
	// HostName:Port.
	out := string(runIceclimber(t, "trust", "--yes", "--config", cfg))
	if !strings.Contains(out, "recorded host key") && !strings.Contains(out, "already trusted") {
		t.Fatalf("trust through jump did not record a key:\n%s", out)
	}
	if b, _ := os.ReadFile(kh); len(b) == 0 {
		t.Fatal("known_hosts is empty after trust through the jump")
	}

	// probe through the jump: a full connect (and fingerprint) over the tunnel.
	pout := string(runIceclimber(t, "probe", "--config", cfg))
	if !strings.Contains(pout, "os/arch:") {
		t.Fatalf("probe through jump did not connect (no fingerprint):\n%s", pout)
	}
}
