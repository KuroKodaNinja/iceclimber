package runtimes

import (
	"path/filepath"
	"testing"
)

func TestParseFlag(t *testing.T) {
	got, err := ParseFlag(" python=system, node=managed ")
	if err != nil {
		t.Fatalf("ParseFlag: %v", err)
	}
	if got["python"].Mode != ModeSystem || got["node"].Mode != ModeManaged {
		t.Errorf("got %+v", got)
	}
	if _, ok := got["java"]; ok {
		t.Error("java should be unset")
	}

	if s, err := ParseFlag(""); err != nil || len(s) != 0 {
		t.Errorf("empty flag = %+v, %v; want empty/no-error", s, err)
	}
	if _, err := ParseFlag("python=bogus"); err == nil {
		t.Error("unknown mode should error")
	}
	if _, err := ParseFlag("ruby=system"); err == nil {
		t.Error("unknown lang should error")
	}
	if _, err := ParseFlag("python"); err == nil {
		t.Error("missing = should error")
	}
	if _, err := ParseFlag("node=system"); err == nil {
		t.Error("system mode for an unsupported lang (node) should error")
	}
	if _, err := ParseFlag("node=managed"); err != nil {
		t.Errorf("node=managed should be accepted: %v", err)
	}
}

func TestSystemSupported(t *testing.T) {
	if !SystemSupported("python") {
		t.Error("python should support system mode")
	}
	if SystemSupported("node") || SystemSupported("java") {
		t.Error("node/java system mode is not implemented yet")
	}
}

func TestResolvePrecedence(t *testing.T) {
	flag := Sources{"python": {Mode: ModeSystem}}
	cfg := Sources{"python": {Mode: ModeManaged}, "node": {Mode: ModeSystem}}
	persisted := Sources{"node": {Mode: ModeManaged}, "java": {Mode: ModeSystem}}
	detected := map[string]bool{"python": true, "node": true, "java": true}

	got := Resolve(flag, cfg, persisted, detected, nil)
	if got["python"].Mode != ModeSystem { // flag wins over config
		t.Errorf("python = %v, want system (flag wins)", got["python"].Mode)
	}
	if got["node"].Mode != ModeSystem { // config wins over persisted
		t.Errorf("node = %v, want system (config wins over persisted)", got["node"].Mode)
	}
	if got["java"].Mode != ModeSystem { // persisted wins over default
		t.Errorf("java = %v, want system (persisted)", got["java"].Mode)
	}
}

func TestResolveDefaultsAndPrompt(t *testing.T) {
	// No explicit choice, no prompt (headless) → managed even when detected.
	got := Resolve(nil, nil, nil, map[string]bool{"python": true}, nil)
	if got["python"].Mode != ModeManaged {
		t.Errorf("headless detected python = %v, want managed default", got["python"].Mode)
	}

	// Prompt fires only for a detected lang with no explicit choice.
	var asked []string
	prompt := func(lang string) Source {
		asked = append(asked, lang)
		return Source{Mode: ModeSystem}
	}
	got = Resolve(nil, nil, nil, map[string]bool{"python": true, "node": false}, prompt)
	if len(asked) != 1 || asked[0] != "python" {
		t.Errorf("prompted for %v, want only python (node not detected)", asked)
	}
	if got["python"].Mode != ModeSystem {
		t.Errorf("python = %v, want system (prompt answer)", got["python"].Mode)
	}
	if got["node"].Mode != ModeManaged {
		t.Errorf("node = %v, want managed (not detected, not prompted)", got["node"].Mode)
	}

	// Operator declines at the prompt (returns an empty Source) → managed default.
	declined := Resolve(nil, nil, nil, map[string]bool{"python": true},
		func(string) Source { return Source{} })
	if declined["python"].Mode != ModeManaged {
		t.Errorf("declined prompt → %v, want managed", declined["python"].Mode)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "runtimes.json")
	st := NewStore(path)

	// Missing file loads as empty, no error.
	if s, err := st.Load(); err != nil || len(s) != 0 {
		t.Fatalf("Load missing = %+v, %v; want empty", s, err)
	}

	want := Sources{"python": {Mode: ModeSystem, EnvManager: "venv"}}
	if err := st.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := st.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got["python"].Mode != ModeSystem || got["python"].EnvManager != "venv" {
		t.Errorf("round-trip = %+v", got)
	}
}

func TestSourcesOf(t *testing.T) {
	s := Sources{"python": {Mode: ModeSystem}}
	if s.Of("python").Mode != ModeSystem {
		t.Error("python should be system")
	}
	if s.Of("node").Mode != ModeManaged {
		t.Error("unset lang should default to managed")
	}
}
