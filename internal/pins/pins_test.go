package pins

import (
	"regexp"
	"strings"
	"testing"
)

// TestRegistryLoadsAndValidates confirms the embedded pins.yaml parses, every runtime line and
// upstream source is present (mustLoad would already panic otherwise), and the security-critical
// pins are well-formed: coursier/runsc must be pinned to an immutable ref (not master/latest) and
// carry correctly-sized checksums, since both execute as code / run as root.
func TestRegistryLoadsAndValidates(t *testing.T) {
	for _, name := range []string{"python", "node", "java", "rust"} {
		if Rt(name).RecommendedLine == "" {
			t.Errorf("%s: empty recommended_line", name)
		}
	}

	cl := P.Tools.CoursierLauncher
	if strings.Contains(cl.URL, "/master/") || strings.Contains(cl.URL, "/raw/master") {
		t.Errorf("coursier launcher must pin an immutable commit, got %q", cl.URL)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(cl.SHA256) {
		t.Errorf("coursier sha256 malformed: %q", cl.SHA256)
	}

	rs := P.Tools.GvisorRunsc
	if rs.Release == "" || strings.Contains(rs.Release, "latest") {
		t.Errorf("runsc must pin a dated release, not %q", rs.Release)
	}
	sha512re := regexp.MustCompile(`^[0-9a-f]{128}$`)
	for _, arch := range []string{"x86_64", "aarch64"} {
		if !sha512re.MatchString(rs.SHA512[arch]) {
			t.Errorf("runsc %s sha512 malformed: %q", arch, rs.SHA512[arch])
		}
	}

	if P.Tools.MavenToolVersion == "" {
		t.Error("empty maven_tool_version")
	}
}
