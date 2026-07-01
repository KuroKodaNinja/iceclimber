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
