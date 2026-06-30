package wire

import (
	"strings"
	"testing"
)

func TestCapabilities_Summary(t *testing.T) {
	host := CapHost{OS: "linux", Arch: "arm64", Libc: "glibc"}

	// Agent present, auth configured → name+version, ✓, and host context.
	c := Capabilities{Host: host, Agent: &CapAgent{
		Name: "claude", DisplayName: "Claude Code", Version: "1.2.3", AuthConfigured: true,
	}}
	got := c.Summary()
	for _, want := range []string{"Claude Code 1.2.3", "auth ✓", "linux/arm64 (glibc)"} {
		if !strings.Contains(got, want) {
			t.Errorf("Summary = %q, want substring %q", got, want)
		}
	}

	// Auth not configured → ✗.
	c.Agent.AuthConfigured = false
	if got := c.Summary(); !strings.Contains(got, "auth ✗") {
		t.Errorf("auth-off summary = %q, want 'auth ✗'", got)
	}

	// No agent yet → placeholder + host.
	if got := (Capabilities{Host: host}).Summary(); !strings.Contains(got, "no agent yet") || !strings.Contains(got, "linux/arm64") {
		t.Errorf("no-agent summary = %q", got)
	}

	// DisplayName falls back to Name.
	if got := (Capabilities{Agent: &CapAgent{Name: "claude"}}).Summary(); !strings.Contains(got, "claude") {
		t.Errorf("name-fallback summary = %q", got)
	}
}

func TestCapHost_String(t *testing.T) {
	cases := map[string]struct {
		h    CapHost
		want string
	}{
		"full":      {CapHost{OS: "linux", Arch: "arm64", Libc: "musl"}, "linux/arm64 (musl)"},
		"no libc":   {CapHost{OS: "linux", Arch: "amd64"}, "linux/amd64"},
		"empty":     {CapHost{}, ""},
		"arch only": {CapHost{Arch: "arm64"}, "arm64"},
	}
	for name, tc := range cases {
		if got := tc.h.String(); got != tc.want {
			t.Errorf("%s: String() = %q, want %q", name, got, tc.want)
		}
	}
}
