package maven

import (
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
)

func TestRef(t *testing.T) {
	cases := map[pkg.Spec]string{
		{Name: "com.google.guava:guava", Version: "33.0.0-jre"}: "com.google.guava:guava:33.0.0-jre",
		{Name: "org.apache.commons:commons-lang3"}:              "org.apache.commons:commons-lang3",
	}
	for s, want := range cases {
		if got := ref(s); got != want {
			t.Errorf("ref(%+v) = %q, want %q", s, got, want)
		}
	}
}

func TestLastNonEmpty(t *testing.T) {
	cases := map[string]string{
		"":                        "",
		"/a.jar:/b.jar":           "/a.jar:/b.jar",
		"warn\n\n/a.jar:/b.jar\n": "/a.jar:/b.jar",
		"  \n  ":                  "",
	}
	for in, want := range cases {
		if got := lastNonEmpty(in); got != want {
			t.Errorf("lastNonEmpty(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveTier(t *testing.T) {
	if resolveTier("relay") != pkg.TierRelay {
		t.Error("explicit relay should stay relay")
	}
	for _, tier := range []string{"", "auto", "mirror"} {
		if resolveTier(tier) != pkg.TierMirror {
			t.Errorf("tier %q should resolve to mirror (Tier 0) for now", tier)
		}
	}
}
