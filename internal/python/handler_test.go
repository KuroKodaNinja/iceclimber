package python

import "testing"

// TestResolveLine: an omitted or "latest" version resolves to the recommended minor (so an
// agent installs the current runtime without guessing), while a specific minor is honored.
func TestResolveLine(t *testing.T) {
	for in, want := range map[string]string{
		"":       RecommendedLine,
		"latest": RecommendedLine,
		"LATEST": RecommendedLine,
		"3.11":   "3.11",
	} {
		if got := ResolveLine(in); got != want {
			t.Errorf("ResolveLine(%q) = %q, want %q", in, got, want)
		}
	}
	if RecommendedLine != "3.12" {
		t.Errorf("Python RecommendedLine = %q, want 3.12", RecommendedLine)
	}
}
