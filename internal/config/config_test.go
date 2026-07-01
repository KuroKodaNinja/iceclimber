package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestLoad_CorporateSSHFields pins the yaml tags of the corporate-SSH keys (the
// #59 rule: a doc/scaffold-named field must be checked by a test), including the
// use_ssh_config *bool tri-state (a typo'd tag would silently ignore the opt-out).
func TestLoad_CorporateSSHFields(t *testing.T) {
	path := writeTemp(t, `sandbox_id: box-1
ssh:
  host: prod
  use_ssh_config: false
  ssh_config_file: ~/.ssh/work_config
  password_auth: true
  keyboard_interactive: true
`)
	cfg, err := Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SSH.UseSSHConfig == nil || *cfg.SSH.UseSSHConfig != false {
		t.Errorf("use_ssh_config = %v, want explicit false", cfg.SSH.UseSSHConfig)
	}
	if !cfg.SSH.PasswordAuth || !cfg.SSH.KeyboardInteractive {
		t.Errorf("password_auth/keyboard_interactive not parsed: %+v", cfg.SSH)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, ".ssh", "work_config"); cfg.SSH.SSHConfigFile != want {
		t.Errorf("ssh_config_file = %q, want expanded %q", cfg.SSH.SSHConfigFile, want)
	}

	// Tri-state: omitting use_ssh_config leaves the pointer nil (= default true),
	// distinct from an explicit false.
	p2 := writeTemp(t, "sandbox_id: box-1\nssh:\n  host: prod\n")
	cfg2, err := Load(p2, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.SSH.UseSSHConfig != nil {
		t.Errorf("omitted use_ssh_config should be nil (default), got %v", *cfg2.SSH.UseSSHConfig)
	}
}

func TestLoad_Runtimes(t *testing.T) {
	path := writeTemp(t, `sandbox_id: box-1
ssh:
  host: prod
runtimes:
  python:
    source: system
    env_manager: conda
  node:
    source: managed
`)
	cfg, err := Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtimes.Python.Source != "system" || cfg.Runtimes.Python.EnvManager != "conda" {
		t.Errorf("python runtime pref = %+v", cfg.Runtimes.Python)
	}
	if cfg.Runtimes.Node.Source != "managed" {
		t.Errorf("node runtime source = %q", cfg.Runtimes.Node.Source)
	}
	if cfg.Runtimes.Java.Source != "" {
		t.Errorf("java runtime source should be unset, got %q", cfg.Runtimes.Java.Source)
	}

	bad := writeTemp(t, "sandbox_id: box-1\nssh:\n  host: prod\nruntimes:\n  python:\n    source: bogus\n")
	if _, err := Load(bad, ""); err == nil {
		t.Error("an invalid runtimes source should be rejected")
	}

	// system mode is python-only for now; node/java=system must be rejected, not a no-op.
	nodeSys := writeTemp(t, "sandbox_id: box-1\nssh:\n  host: prod\nruntimes:\n  node:\n    source: system\n")
	if _, err := Load(nodeSys, ""); err == nil {
		t.Error("node source=system should be rejected (only python supports system mode)")
	}

	// env_manager must be venv|conda, and only python has one.
	badMgr := writeTemp(t, "sandbox_id: box-1\nssh:\n  host: prod\nruntimes:\n  python:\n    source: system\n    env_manager: poetry\n")
	if _, err := Load(badMgr, ""); err == nil {
		t.Error("an unknown env_manager should be rejected")
	}
	nodeMgr := writeTemp(t, "sandbox_id: box-1\nssh:\n  host: prod\nruntimes:\n  node:\n    env_manager: conda\n")
	if _, err := Load(nodeMgr, ""); err == nil {
		t.Error("env_manager on a non-python runtime should be rejected")
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

func TestRetention(t *testing.T) {
	cases := map[string]struct {
		val  string
		want time.Duration
	}{
		"default when unset": {"", time.Hour},
		"explicit":           {"30m", 30 * time.Minute},
		"disabled":           {"0", 0},
		"invalid → default":  {"not-a-duration", time.Hour},
	}
	for name, tc := range cases {
		if got := (&Config{MaildirRetention: tc.val}).Retention(); got != tc.want {
			t.Errorf("%s: Retention(%q) = %v, want %v", name, tc.val, got, tc.want)
		}
	}
}

func TestEgressMode(t *testing.T) {
	// Defaults: relay (proxy off), default port.
	c := &Config{}
	if c.EgressProxy() {
		t.Error("empty egress_mode should be relay (proxy off)")
	}
	if c.EgressPort() != DefaultEgressProxyPort {
		t.Errorf("EgressPort default = %d, want %d", c.EgressPort(), DefaultEgressProxyPort)
	}
	// Proxy mode + explicit port.
	c = &Config{EgressMode: "proxy", EgressProxyPort: 9999}
	if !c.EgressProxy() || c.EgressPort() != 9999 {
		t.Errorf("proxy mode = %v, port = %d", c.EgressProxy(), c.EgressPort())
	}
	// Validation: relay/proxy/"" accepted, anything else rejected.
	for _, m := range []string{"", "relay", "proxy", "PROXY"} {
		if err := (&Config{SandboxID: "s", SSH: SSH{Host: "h"}, RemoteRoot: "/r", EgressMode: m}).validate(""); err != nil {
			t.Errorf("egress_mode %q should validate: %v", m, err)
		}
	}
	if err := (&Config{SandboxID: "s", SSH: SSH{Host: "h"}, RemoteRoot: "/r", EgressMode: "vpn"}).validate(""); err == nil {
		t.Error("egress_mode \"vpn\" should be rejected")
	}
}
