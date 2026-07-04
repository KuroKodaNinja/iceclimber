package catalog

import "testing"

// validEntry is a minimal entry that passes validate() — the base each guard case mutates.
func validEntry() Entry {
	return Entry{
		Name: "t", Verify: "t --version", Kind: KindStaticPinned, Version: "1.0",
		Platforms: map[string]Platform{"linux/x86_64/musl": {URL: "https://mirror/t", SHA256: "ab"}},
		Unpack:    Unpack{Format: "binary", Bins: []string{"t"}},
	}
}

// TestValidateRejectsTraversal: dest + bins are joined under <root> (and bins are linked into
// tools/bin), so an absolute or ".." component would escape the sandbox install root. validate()
// must reject it at load — the zip-slip floor for operator/overlay catalog entries.
func TestValidateRejectsTraversal(t *testing.T) {
	base := validEntry()
	if err := base.validate(); err != nil {
		t.Fatalf("base entry should validate: %v", err)
	}

	bad := []struct {
		name   string
		mutate func(*Entry)
	}{
		{"dest ..", func(e *Entry) { e.Dest = "../../etc" }},
		{"dest absolute", func(e *Entry) { e.Dest = "/etc" }},
		{"dest nested ..", func(e *Entry) { e.Dest = "tools/../../etc" }},
		{"bin ..", func(e *Entry) { e.Unpack.Bins = []string{"../../etc/x"} }},
		{"bin absolute", func(e *Entry) { e.Unpack.Bins = []string{"/etc/x"} }},
		{"bin empty", func(e *Entry) { e.Unpack.Bins = []string{""} }},
	}
	for _, c := range bad {
		e := validEntry()
		c.mutate(&e)
		if err := e.validate(); err == nil {
			t.Errorf("%s: validate() accepted an escaping path, want error", c.name)
		}
	}

	// A legitimate nested dest/bin is still fine.
	ok := validEntry()
	ok.Dest = "tools/sub"
	ok.Unpack.Bins = []string{"bin/t"}
	if err := ok.validate(); err != nil {
		t.Errorf("legitimate nested dest/bin should validate: %v", err)
	}
}
