package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/xdg"
)

// writeRegistrySandbox drops a minimal valid config.yaml under ~/.config/iceclimber/<id>/ so
// the registry scanner can find it. managed adds a provider block (iceclimber-managed local).
func writeRegistrySandbox(t *testing.T, id string, managed bool) {
	t.Helper()
	dir := xdg.SandboxDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "sandbox_id: " + id + "\nssh:\n  host: 127.0.0.1\n  port: 2222\n  use_ssh_config: false\n"
	if managed {
		body += "provider:\n  type: lima\n  instance: iceclimber-" + id + "\n  distro: ubuntu\n  arch: aarch64\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// isolateRegistry points XDG at temp dirs and resets the global --sandbox selector, so each
// test gets a clean registry with no cross-talk.
func isolateRegistry(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	old := sandboxID
	sandboxID = ""
	t.Cleanup(func() { sandboxID = old })
}

// TestListSandboxesScansRegistry: list finds every dir with a config.yaml, skips dirs
// without one (and stray files), and distinguishes managed (provider) from remote.
func TestListSandboxesScansRegistry(t *testing.T) {
	isolateRegistry(t)
	writeRegistrySandbox(t, "dev", true)   // managed
	writeRegistrySandbox(t, "prod", false) // remote
	// A dir without config.yaml is NOT a sandbox.
	if err := os.MkdirAll(filepath.Join(xdg.Config(), "notasandbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A stray top-level file (like the default pointer) must be ignored.
	if err := os.WriteFile(filepath.Join(xdg.Config(), "default"), []byte("dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries := listSandboxes()
	if len(entries) != 2 {
		t.Fatalf("listSandboxes() found %d entries, want 2: %+v", len(entries), entries)
	}
	byID := map[string]sandboxEntry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	dev, ok := byID["dev"]
	if !ok {
		t.Fatal("dev not found")
	}
	if !dev.managed() {
		t.Error("dev should be managed (has a provider block)")
	}
	if dev.Cfg.Provider.Instance != "iceclimber-dev" {
		t.Errorf("dev provider instance = %q, want iceclimber-dev", dev.Cfg.Provider.Instance)
	}
	prod, ok := byID["prod"]
	if !ok {
		t.Fatal("prod not found")
	}
	if prod.managed() {
		t.Error("prod should be remote (no provider block)")
	}
}

// TestListSandboxesEmpty: a fresh (or absent) registry lists nothing without erroring.
func TestListSandboxesEmpty(t *testing.T) {
	isolateRegistry(t)
	if got := listSandboxes(); len(got) != 0 {
		t.Errorf("listSandboxes() on empty registry = %d entries, want 0", len(got))
	}
}

// TestDefaultPointerRoundTrip: write/read/clear of the ~/.config/iceclimber/default pointer,
// and defaultSandbox() falling back to it when --sandbox is unset.
func TestDefaultPointerRoundTrip(t *testing.T) {
	isolateRegistry(t)
	if got := readDefaultSandbox(); got != "" {
		t.Errorf("readDefaultSandbox() on empty = %q, want \"\"", got)
	}
	if err := writeDefaultSandbox("dev"); err != nil {
		t.Fatal(err)
	}
	if got := readDefaultSandbox(); got != "dev" {
		t.Errorf("readDefaultSandbox() = %q, want dev", got)
	}
	if got := defaultSandbox(); got != "dev" {
		t.Errorf("defaultSandbox() = %q, want dev (from pointer)", got)
	}
	// An explicit --sandbox wins over the pointer.
	sandboxID = "other"
	if got := defaultSandbox(); got != "other" {
		t.Errorf("defaultSandbox() with --sandbox = %q, want other", got)
	}
	sandboxID = ""
	clearDefaultSandbox()
	if got := readDefaultSandbox(); got != "" {
		t.Errorf("readDefaultSandbox() after clear = %q, want \"\"", got)
	}
}

// TestListMarksDefault: the list row for the default sandbox is flagged.
func TestListMarksDefault(t *testing.T) {
	isolateRegistry(t)
	writeRegistrySandbox(t, "dev", true)
	writeRegistrySandbox(t, "prod", false)
	if err := writeDefaultSandbox("prod"); err != nil {
		t.Fatal(err)
	}
	def := readDefaultSandbox()
	for _, e := range listSandboxes() {
		r := sandboxListRowFor(e, def)
		if r.Name == "prod" && !r.Default {
			t.Error("prod row should be marked default")
		}
		if r.Name == "dev" && r.Default {
			t.Error("dev row should NOT be marked default")
		}
		// Provider strings: managed → lima:ubuntu, remote → "remote".
		if r.Name == "dev" && r.Provider != "lima:ubuntu" {
			t.Errorf("dev provider row = %q, want lima:ubuntu", r.Provider)
		}
		if r.Name == "prod" && r.Provider != "remote" {
			t.Errorf("prod provider row = %q, want remote", r.Provider)
		}
	}
}

// TestNextCreateName: dev when free, then dev-2, dev-3 as the registry fills.
func TestNextCreateName(t *testing.T) {
	isolateRegistry(t)
	if got := nextCreateName(); got != "dev" {
		t.Fatalf("nextCreateName() on empty = %q, want dev", got)
	}
	writeRegistrySandbox(t, "dev", true)
	if got := nextCreateName(); got != "dev-2" {
		t.Fatalf("nextCreateName() with dev taken = %q, want dev-2", got)
	}
	writeRegistrySandbox(t, "dev-2", true)
	if got := nextCreateName(); got != "dev-3" {
		t.Fatalf("nextCreateName() with dev,dev-2 taken = %q, want dev-3", got)
	}
}

// TestValidSandboxName rejects the ids that would break the registry / default pointer.
func TestValidSandboxName(t *testing.T) {
	good := []string{"dev", "dev-2", "my-box", "box1"}
	for _, id := range good {
		if err := validSandboxName(id); err != nil {
			t.Errorf("validSandboxName(%q) = %v, want nil", id, err)
		}
	}
	bad := []string{"", "all", "default", "a/b", "a\\b", ".hidden"}
	for _, id := range bad {
		if err := validSandboxName(id); err == nil {
			t.Errorf("validSandboxName(%q) = nil, want error", id)
		}
	}
}

// TestNewManagedConfigRoundTrips: the config `sandbox create` writes marshals and reloads
// through config.Load with the provider block, proxy egress, and direct-dial SSH intact —
// i.e. what create writes is exactly what the rest of the CLI reads back.
func TestNewManagedConfigRoundTrips(t *testing.T) {
	isolateRegistry(t)
	cfg := newManagedConfig("dev", "iceclimber-dev", "ubuntu", "127.0.0.1", 60022, "bob", "/id", "/kh")
	if err := writeSandboxConfig(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(sandboxConfigPath("dev"), "dev")
	if err != nil {
		t.Fatalf("reload written config: %v", err)
	}
	if got.SandboxID != "dev" {
		t.Errorf("sandbox_id = %q, want dev", got.SandboxID)
	}
	if !got.EgressProxy() {
		t.Error("egress should be proxy mode")
	}
	if got.Provider == nil || got.Provider.Type != "lima" || got.Provider.Instance != "iceclimber-dev" || got.Provider.Distro != "ubuntu" {
		t.Errorf("provider block wrong: %+v", got.Provider)
	}
	if got.SSH.Host != "127.0.0.1" || got.SSH.Port != 60022 || got.SSH.KnownHosts != "/kh" {
		t.Errorf("ssh block wrong: %+v", got.SSH)
	}
	if got.SSH.UseSSHConfig == nil || *got.SSH.UseSSHConfig {
		t.Error("use_ssh_config should be explicitly false (direct dial)")
	}
}
