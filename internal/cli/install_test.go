package cli

import "testing"

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
