package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
)

// TestDialConfigMapping pins that every config.SSH field reaches remote.DialConfig
// through the single dialConfig funnel — a dropped field would silently no-op for
// ALL four dial sites (keyboard_interactive in particular is exercised by nothing
// else). #59: the config keys are documented, so a mapping gap must be caught here.
func TestDialConfigMapping(t *testing.T) {
	yes := true
	cfg := &config.Config{SSH: config.SSH{
		Host: "h", Port: 2200, User: "u", IdentityFile: "/k", KnownHosts: "/kh",
		SSHConfigFile: "/cfg", UseSSHConfig: &yes, PasswordAuth: true, KeyboardInteractive: true,
	}}
	dc := dialConfig(cfg)
	switch {
	case dc.Host != "h" || dc.Port != 2200 || dc.User != "u":
		t.Errorf("host/port/user not mapped: %+v", dc)
	case dc.IdentityFile != "/k" || dc.KnownHosts != "/kh" || dc.SSHConfigFile != "/cfg":
		t.Errorf("identity/knownhosts/configfile not mapped: %+v", dc)
	case dc.UseSSHConfig == nil || *dc.UseSSHConfig != true:
		t.Errorf("use_ssh_config not mapped: %+v", dc.UseSSHConfig)
	case !dc.AllowPassword || !dc.AllowKeyboardInteractive:
		t.Errorf("password/keyboard-interactive opt-ins not mapped: %+v", dc)
	}
}

// TestScaffoldParses: the `iceclimber init` template must load cleanly through
// config.Load (a malformed scaffold or a renamed key would break new users).
func TestScaffoldParses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iceclimber.yaml")
	if err := os.WriteFile(path, []byte(scaffoldYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path, ""); err != nil {
		t.Fatalf("init scaffold does not parse/validate: %v", err)
	}
}
