package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/probe"
	"github.com/KuroKodaNinja/iceclimber/internal/runtimes"
	"github.com/KuroKodaNinja/iceclimber/internal/tui"
)

// TestRuntimeSourceLazyResolveRoundTrip proves the console's runtime-source modal
// path end to end without a VM: the session resolves the source LAZILY, so a choice
// persisted via SetRuntimeSources is picked up by the next resolve — no reconnect or
// cached-staleness. Also covers DetectedRuntimes filtering to supported langs.
func TestRuntimeSourceLazyResolveRoundTrip(t *testing.T) {
	store := runtimes.NewStore(filepath.Join(t.TempDir(), "runtimes.json"))
	sess := &session{
		fp: &probe.Fingerprint{Runtimes: []probe.RuntimeInfo{
			{Lang: "python", Version: "3.12.1", Path: "/usr/bin/python3"},
			{Lang: "node", Version: "20.1.0", Path: "/usr/bin/node"},
		}},
		runtimeStore:  store,
		runtimeConfig: runtimes.Sources{},
	}
	if sess.runtimeSourcesNow().Of("python").Mode != runtimes.ModeManaged {
		t.Fatal("default source should be managed")
	}

	holder := &sessionHolder{}
	holder.Set(sess)
	ops := &consoleOps{ctx: context.Background(), holder: holder}

	// Every detected, system-supported runtime is offered (python, node, java).
	dr := ops.DetectedRuntimes()
	langs := map[string]string{}
	for _, r := range dr {
		langs[r.Lang] = r.Version
	}
	if langs["python"] != "3.12.1" || langs["node"] != "20.1.0" {
		t.Fatalf("DetectedRuntimes = %+v, want python + node offered", dr)
	}

	if err := ops.SetRuntimeSources(map[string]tui.RuntimeSelection{"python": {System: true, EnvManager: "conda"}}); err != nil {
		t.Fatalf("SetRuntimeSources: %v", err)
	}
	// The lazy resolve re-reads the store, so the new choice is live immediately.
	src := sess.runtimeSourcesNow().Of("python")
	if src.Mode != runtimes.ModeSystem {
		t.Errorf("after SetRuntimeSources(system), source = %q, want system", src.Mode)
	}
	if src.EnvManager != "conda" {
		t.Errorf("env_manager = %q, want conda (persisted from the modal)", src.EnvManager)
	}
}

// TestOverlayRuntimeSources is the cmdline parity for the console modal's SetRuntimeSources:
// `install --runtime-source` overlays the SAME store, leaving unnamed languages untouched.
// Pairs with TestRuntimeSourceLazyResolveRoundTrip (the TUI side) and the glibc functional
// TestInstallRuntimeSourceFlag (live) so both surfaces of the source choice are covered.
func TestOverlayRuntimeSources(t *testing.T) {
	store := runtimes.NewStore(filepath.Join(t.TempDir(), "runtimes.json"))
	// Seed a pre-existing choice the overlay must preserve.
	if err := store.Save(runtimes.Sources{"java": {Mode: runtimes.ModeSystem}}); err != nil {
		t.Fatal(err)
	}

	// Empty flag is a no-op (doesn't clobber the seed).
	if err := overlayRuntimeSources(store, "  "); err != nil {
		t.Fatalf("empty overlay should be a no-op: %v", err)
	}

	// A real flag overlays python (with env_manager) + node, keeping java.
	if err := overlayRuntimeSources(store, "python=system:conda,node=managed"); err != nil {
		t.Fatalf("overlayRuntimeSources: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got["python"].Mode != runtimes.ModeSystem || got["python"].EnvManager != "conda" {
		t.Errorf("python = %+v, want system:conda", got["python"])
	}
	if got["node"].Mode != runtimes.ModeManaged {
		t.Errorf("node = %+v, want managed", got["node"])
	}
	if got["java"].Mode != runtimes.ModeSystem {
		t.Errorf("java = %+v, want system preserved (overlay must not drop unnamed langs)", got["java"])
	}

	// A malformed flag errors (and the ParseFlag guard is exercised on the CLI path too).
	if err := overlayRuntimeSources(store, "ruby=system"); err == nil {
		t.Error("overlayRuntimeSources should reject an unsupported language")
	}
}
