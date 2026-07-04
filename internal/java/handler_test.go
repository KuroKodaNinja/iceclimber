package java

import "testing"

// TestResolveLine: an omitted or "latest" version resolves to the recommended feature version
// (so an agent installs the current JDK without guessing), while a specific one is honored.
func TestResolveLine(t *testing.T) {
	for in, want := range map[string]string{
		"":       RecommendedLine,
		"latest": RecommendedLine,
		"LATEST": RecommendedLine,
		"17":     "17",
	} {
		if got := ResolveLine(in); got != want {
			t.Errorf("ResolveLine(%q) = %q, want %q", in, got, want)
		}
	}
	if RecommendedLine != "21" {
		t.Errorf("Java RecommendedLine = %q, want 21 (LTS)", RecommendedLine)
	}
}
