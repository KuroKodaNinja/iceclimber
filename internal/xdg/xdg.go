// Package xdg resolves iceclimber's base directories per the XDG Base Directory spec: config +
// per-sandbox state live under $XDG_CONFIG_HOME/iceclimber (default ~/.config/iceclimber) and the
// shared artifact cache under $XDG_CACHE_HOME/iceclimber (default ~/.cache/iceclimber). It is the
// SINGLE source for these paths — every consumer (config search, approvals, activity, audit,
// runtimes, locks, egress certs, catalog overlays, the cache) reads from here instead of
// hand-joining "~/.iceclimber", so a future relocation (or a fleet path change) is one edit.
package xdg

import (
	"os"
	"path/filepath"
)

const app = "iceclimber"

// Config is the config/state root: $XDG_CONFIG_HOME/iceclimber, else ~/.config/iceclimber.
func Config() string { return filepath.Join(base("XDG_CONFIG_HOME", ".config"), app) }

// Cache is the artifact-cache root: $XDG_CACHE_HOME/iceclimber, else ~/.cache/iceclimber.
func Cache() string { return filepath.Join(base("XDG_CACHE_HOME", ".cache"), app) }

// SandboxDir is one sandbox's per-sandbox directory under Config() — config.yaml + approvals +
// activity + runtimes + agent.log + the .popo.lock all live here.
func SandboxDir(id string) string { return filepath.Join(Config(), id) }

// base returns $envVar when it holds an ABSOLUTE path (the XDG spec ignores a relative/empty
// value), else ~/<fallback>. Falls back to the bare fallback if the home dir is unavailable.
func base(envVar, fallback string) string {
	if v := os.Getenv(envVar); filepath.IsAbs(v) {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fallback
	}
	return filepath.Join(home, fallback)
}

// Migrate performs the one-time move of a legacy ~/.iceclimber to the XDG Config() root — once,
// only when the new root doesn't yet exist — so an existing operator's approvals/runtime choices
// carry over. It returns a human notice (or "") for the caller to print. Best-effort: any
// failure (or nothing to migrate) returns "" and is non-fatal.
func Migrate() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	legacy := filepath.Join(home, "."+app)
	newRoot := Config()
	if legacy == newRoot {
		return ""
	}
	if _, err := os.Stat(newRoot); err == nil {
		return "" // new root already exists — leave both alone
	}
	if fi, err := os.Stat(legacy); err != nil || !fi.IsDir() {
		return "" // no legacy dir to migrate
	}
	if err := os.MkdirAll(filepath.Dir(newRoot), 0o755); err != nil {
		return ""
	}
	if err := os.Rename(legacy, newRoot); err != nil {
		return ""
	}
	return "iceclimber: migrated " + legacy + " → " + newRoot
}
