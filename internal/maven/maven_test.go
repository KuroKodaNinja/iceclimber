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

func TestLooksLikeProxyDown(t *testing.T) {
	down := []string{
		"[ERROR] Failed to execute goal ... Connection refused",
		"Caused by: java.net.ConnectException: Connection refused (Connection refused)",
		"Could not transfer artifact ... Connect to 127.0.0.1:18080 failed",
	}
	for _, s := range down {
		if !looksLikeProxyDown(s) {
			t.Errorf("looksLikeProxyDown(%q) = false, want true", s)
		}
	}
	notDown := []string{
		"[ERROR] COMPILATION ERROR : cannot find symbol",
		"[ERROR] Failed to execute goal on project: dependency X was not found in central",
		"",
	}
	for _, s := range notDown {
		if looksLikeProxyDown(s) {
			t.Errorf("looksLikeProxyDown(%q) = true, want false (a real build error, not a proxy-down)", s)
		}
	}
}

func TestResolveTier(t *testing.T) {
	// Explicit choices are forced regardless of config.
	if resolveTier("relay", "https://mirror") != pkg.TierRelay {
		t.Error("explicit relay should stay relay")
	}
	if resolveTier("mirror", "") != pkg.TierMirror {
		t.Error("explicit mirror should stay mirror")
	}
	// auto: relay when no sandbox-reachable repo (air-gap default), else mirror.
	for _, tier := range []string{"", "auto"} {
		if resolveTier(tier, "") != pkg.TierRelay {
			t.Errorf("tier %q with no mirror should pick relay (air-gap default)", tier)
		}
		if resolveTier(tier, "https://nexus.corp/maven") != pkg.TierMirror {
			t.Errorf("tier %q with a mirror should pick mirror", tier)
		}
	}
}
