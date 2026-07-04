package node

import "testing"

// TestResolveLine: an omitted or "latest" version resolves to the recommended line (so an
// agent installs the current runtime without guessing), while a specific line is honored.
func TestResolveLine(t *testing.T) {
	for in, want := range map[string]string{
		"":         RecommendedLine,
		"  ":       RecommendedLine,
		"latest":   RecommendedLine,
		"LATEST":   RecommendedLine,
		" latest ": RecommendedLine,
		"22":       "22",
		"20.11":    "20.11",
	} {
		if got := ResolveLine(in); got != want {
			t.Errorf("ResolveLine(%q) = %q, want %q", in, got, want)
		}
	}
	if RecommendedLine != "24" {
		t.Errorf("Node RecommendedLine = %q, want 24 (musl arm64 needs ≥ 24)", RecommendedLine)
	}
}
