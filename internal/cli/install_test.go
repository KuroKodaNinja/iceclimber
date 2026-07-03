package cli

import (
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/node"
)

func TestParseCoords(t *testing.T) {
	specs, err := parseCoords([]string{"com.google.guava:guava:33.0.0-jre", "org.apache.commons:commons-lang3:3.14.0"})
	if err != nil {
		t.Fatalf("parseCoords: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("got %d specs, want 2", len(specs))
	}
	if specs[0].Name != "com.google.guava:guava" || specs[0].Version != "33.0.0-jre" {
		t.Errorf("specs[0] = %+v", specs[0])
	}

	for _, bad := range []string{"guava", "g:a", "g:a:", ":a:v", "g::v"} {
		if _, err := parseCoords([]string{bad}); err == nil {
			t.Errorf("parseCoords(%q) should error (want group:artifact:version)", bad)
		}
	}
}

// TestOptionalVersion: an omitted (or "latest") runtime version resolves to the
// recommended line — the CLI parity for the popo/console affordance (decision #83).
func TestOptionalVersion(t *testing.T) {
	if got := optionalVersion(nil, node.ResolveLine); got != node.RecommendedLine {
		t.Errorf("omitted = %q, want the recommended line %q", got, node.RecommendedLine)
	}
	if got := optionalVersion([]string{"latest"}, node.ResolveLine); got != node.RecommendedLine {
		t.Errorf("latest = %q, want %q", got, node.RecommendedLine)
	}
	if got := optionalVersion([]string{"22"}, node.ResolveLine); got != "22" {
		t.Errorf("explicit = %q, want it verbatim", got)
	}
}
