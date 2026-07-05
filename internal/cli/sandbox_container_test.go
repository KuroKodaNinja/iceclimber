package cli

import (
	"os"
	"path/filepath"
	"testing"
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
