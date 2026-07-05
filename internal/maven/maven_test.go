package maven

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestControllerLauncher_PinnedAndVerified is the supply-chain guard for the Coursier
// launcher (executed as code): the source URL must be an immutable commit (not `master`), and
// a download whose bytes don't match the pinned SHA-256 must fail closed — never get cached or
// run.
func TestControllerLauncher_PinnedAndVerified(t *testing.T) {
	if strings.Contains(coursierLauncherURL, "/master/") || strings.Contains(coursierLauncherURL, "/raw/master") {
		t.Error("coursier launcher must be pinned to an immutable commit, not master")
	}
	if coursierLauncherSHA256 == "" {
		t.Fatal("coursier launcher must carry a pinned SHA-256")
	}
	m := New(Config{
		CacheDir: t.TempDir(),
		HTTPClient: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte("TAMPERED-LAUNCHER")))}, nil
		})},
	})
	_, err := m.controllerLauncher(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("a launcher whose bytes fail the pin must be rejected, got err=%v", err)
	}
}

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
