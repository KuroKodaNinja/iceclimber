package xdg

import (
	"os"
	"path/filepath"
	"testing"
)

// TestConfigCacheAbsoluteEnv: an absolute $XDG_*_HOME is honored verbatim (with the
// iceclimber app segment appended); SandboxDir is Config()/id.
func TestConfigCacheAbsoluteEnv(t *testing.T) {
	cfgHome := t.TempDir()
	cacheHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	if got, want := Config(), filepath.Join(cfgHome, "iceclimber"); got != want {
		t.Errorf("Config() = %q, want %q", got, want)
	}
	if got, want := Cache(), filepath.Join(cacheHome, "iceclimber"); got != want {
		t.Errorf("Cache() = %q, want %q", got, want)
	}
	if got, want := SandboxDir("box1"), filepath.Join(Config(), "box1"); got != want {
		t.Errorf("SandboxDir() = %q, want %q", got, want)
	}
}

// TestConfigCacheRelativeEnvIgnored: a relative $XDG_*_HOME is ignored per the spec —
// fall back to ~/.config and ~/.cache.
func TestConfigCacheRelativeEnvIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "relative/config")
	t.Setenv("XDG_CACHE_HOME", "relative/cache")

	if got, want := Config(), filepath.Join(home, ".config", "iceclimber"); got != want {
		t.Errorf("Config() = %q, want %q", got, want)
	}
	if got, want := Cache(), filepath.Join(home, ".cache", "iceclimber"); got != want {
		t.Errorf("Cache() = %q, want %q", got, want)
	}
}

// TestMigrateMovesLegacy: a legacy ~/.iceclimber dir is renamed to Config() when Config()
// doesn't yet exist; the notice is non-empty; a second call is a no-op returning "".
func TestMigrateMovesLegacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Point XDG_CONFIG_HOME at a subdir that does NOT exist yet, so Config() is absent.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdgcfg"))

	legacy := filepath.Join(home, ".iceclimber")
	if err := os.MkdirAll(filepath.Join(legacy, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(legacy, "sub", "marker")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	notice := Migrate()
	if notice == "" {
		t.Fatal("Migrate() returned empty notice, want a migration message")
	}
	// Legacy is gone; content moved under Config().
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy dir still present after migrate: err=%v", err)
	}
	moved := filepath.Join(Config(), "sub", "marker")
	if _, err := os.Stat(moved); err != nil {
		t.Errorf("migrated marker not found at %q: %v", moved, err)
	}

	// Second call: nothing to migrate (Config() now exists / legacy gone) → "".
	if n := Migrate(); n != "" {
		t.Errorf("second Migrate() = %q, want \"\"", n)
	}
}

// TestMigrateNoOpWhenConfigExists: when Config() already exists, Migrate leaves both dirs
// in place and returns "".
func TestMigrateNoOpWhenConfigExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdgcfg"))

	legacy := filepath.Join(home, ".iceclimber")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(Config(), 0o755); err != nil {
		t.Fatal(err)
	}

	if n := Migrate(); n != "" {
		t.Errorf("Migrate() = %q, want \"\" when Config() already exists", n)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("legacy dir should be left in place: %v", err)
	}
	if _, err := os.Stat(Config()); err != nil {
		t.Errorf("Config() dir should be left in place: %v", err)
	}
}
