package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "iceclimber.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_Valid(t *testing.T) {
	path := writeTemp(t, `
sandbox_id: box-1
ssh:
  host: example.internal
  user: agent
  known_hosts: ~/.ssh/known_hosts
remote_root: /home/agent/.iceclimber
`)
	cfg, err := Load(path, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SandboxID != "box-1" || cfg.SSH.Host != "example.internal" {
		t.Errorf("unexpected config: %+v", cfg)
	}
	// Port is intentionally left 0 (unset) when omitted — the dial layer applies the
	// 22 default last, so an ssh_config Port can win during resolution.
	if cfg.SSH.Port != 0 {
		t.Errorf("Port = %d, want 0 (unset; defaulted at dial time)", cfg.SSH.Port)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, ".ssh", "known_hosts"); cfg.SSH.KnownHosts != want {
		t.Errorf("KnownHosts = %q, want expanded %q", cfg.SSH.KnownHosts, want)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	path := writeTemp(t, "sandbox_id: box-1\n")
	_, err := Load(path, "")
	if err == nil {
		t.Fatal("expected error for missing ssh.host")
	}
	if !strings.Contains(err.Error(), "ssh.host") {
		t.Errorf("error should name the missing ssh.host, got: %v", err)
	}
}

// TestLoad_RejectsDashHost: a host starting with '-' would be parsed by `ssh -G`
// as an option flag (option injection), so Load must reject it.
func TestLoad_RejectsDashHost(t *testing.T) {
	path := writeTemp(t, "sandbox_id: box-1\nssh:\n  host: -oProxyCommand=evil\n  user: u\n")
	if _, err := Load(path, ""); err == nil || !strings.Contains(err.Error(), "must not start with '-'") {
		t.Fatalf("want rejection of dash-host, got: %v", err)
	}
}

// TestLoad_UserOptional: ssh.user is no longer required — ssh_config or the OS
// default can supply it, so a config with only ssh.host loads cleanly.
func TestLoad_UserOptional(t *testing.T) {
	path := writeTemp(t, "sandbox_id: box-1\nssh:\n  host: example.internal\n")
	cfg, err := Load(path, "")
	if err != nil {
		t.Fatalf("ssh.user should be optional; Load failed: %v", err)
	}
	if cfg.SSH.User != "" {
		t.Errorf("User = %q, want empty (resolved later)", cfg.SSH.User)
	}
}

func TestLoad_SandboxMismatch(t *testing.T) {
	path := writeTemp(t, "sandbox_id: box-1\nssh:\n  host: h\n  user: u\n")
	if _, err := Load(path, "box-2"); err == nil {
		t.Fatal("expected mismatch error")
	}
	if _, err := Load(path, "box-1"); err != nil {
		t.Errorf("matching --sandbox should pass, got: %v", err)
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	tests := []struct{ in, want string }{
		{"~/foo", filepath.Join(home, "foo")},
		{"~", home},
		{"/abs/path", "/abs/path"},
		{"", ""},
		{"~user/foo", "~user/foo"}, // ~user form unsupported, returned verbatim
	}
	for _, tt := range tests {
		if got := expandHome(tt.in); got != tt.want {
			t.Errorf("expandHome(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
