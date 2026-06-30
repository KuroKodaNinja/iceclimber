package pip

import (
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
)

func TestPlatformTags(t *testing.T) {
	if got := platformTags("aarch64", "musl"); got[0] != "musllinux_1_2_aarch64" {
		t.Errorf("musl tags = %v", got)
	}
	if got := platformTags("x86_64", "glibc"); got[0] != "manylinux_2_28_x86_64" {
		t.Errorf("glibc tags = %v", got)
	}
}

func TestDownloadArgs(t *testing.T) {
	args := strings.Join(downloadArgs(
		[]pkg.Spec{{Name: "markupsafe", Version: "2.1.5"}, {Name: "rich"}},
		"3.12", "aarch64", "musl", "https://pypi.org/simple", "/tmp/wh",
		[]string{"--index-url", "https://download.pytorch.org/whl/cpu", "--pre"}), " ")
	for _, want := range []string{
		"-m pip download", "--only-binary=:all:", "--dest /tmp/wh",
		"--python-version 3.12", "--abi cp312", "--implementation cp",
		"--platform musllinux_1_2_aarch64", "--platform musllinux_1_1_aarch64",
		"--index-url https://pypi.org/simple", "markupsafe==2.1.5", "rich",
		// agent extra_args appended after the default index (agent --index-url wins) + before specs
		"--index-url https://download.pytorch.org/whl/cpu", "--pre",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("downloadArgs missing %q in:\n%s", want, args)
		}
	}
}

func TestWheelNameVersion(t *testing.T) {
	tests := []struct{ file, name, version string }{
		{"charset_normalizer-3.4.7-cp312-cp312-musllinux_1_2_aarch64.whl", "charset_normalizer", "3.4.7"},
		{"requests-2.34.2-py3-none-any.whl", "requests", "2.34.2"},
	}
	for _, tt := range tests {
		n, v := wheelNameVersion(tt.file)
		if n != tt.name || v != tt.version {
			t.Errorf("wheelNameVersion(%q) = %q,%q; want %q,%q", tt.file, n, v, tt.name, tt.version)
		}
	}
}

func TestNormName(t *testing.T) {
	for in, want := range map[string]string{
		"charset_normalizer": "charset-normalizer",
		"charset-normalizer": "charset-normalizer",
		"Jinja2":             "jinja2",
		"ruamel.yaml":        "ruamel-yaml",
	} {
		if got := normName(in); got != want {
			t.Errorf("normName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveTier(t *testing.T) {
	tests := []struct {
		tier     string
		hasIndex bool
		want     string
	}{
		{"auto", true, pkg.TierMirror},
		{"auto", false, pkg.TierRelay},
		{"", true, pkg.TierMirror},
		{"relay", true, pkg.TierRelay}, // forced
		{"mirror", false, pkg.TierMirror},
	}
	for _, tt := range tests {
		if got := resolveTier(tt.tier, tt.hasIndex); got != tt.want {
			t.Errorf("resolveTier(%q,%v) = %q, want %q", tt.tier, tt.hasIndex, got, tt.want)
		}
	}
}
