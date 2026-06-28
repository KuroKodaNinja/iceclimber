package java

import (
	"encoding/json"
	"testing"
)

func TestAdoptiumPlatform(t *testing.T) {
	tests := []struct {
		os, arch, libc   string
		wantArch, wantOS string
		wantErr          bool
	}{
		{"linux", "x86_64", "glibc", "x64", "linux", false},
		{"linux", "aarch64", "glibc", "aarch64", "linux", false},
		{"linux", "x86_64", "musl", "x64", "alpine-linux", false},
		{"linux", "aarch64", "musl", "aarch64", "alpine-linux", false},
		{"darwin", "x86_64", "glibc", "", "", true},
		{"linux", "riscv64", "glibc", "", "", true},
		{"linux", "x86_64", "uclibc", "", "", true},
	}
	for _, tt := range tests {
		a, o, err := adoptiumPlatform(tt.os, tt.arch, tt.libc)
		if tt.wantErr {
			if err == nil {
				t.Errorf("adoptiumPlatform(%s,%s,%s): want error", tt.os, tt.arch, tt.libc)
			}
			continue
		}
		if err != nil || a != tt.wantArch || o != tt.wantOS {
			t.Errorf("adoptiumPlatform(%s,%s,%s) = %q,%q,%v; want %q,%q",
				tt.os, tt.arch, tt.libc, a, o, err, tt.wantArch, tt.wantOS)
		}
	}
}

const adoptiumSample = `[
  {"binary":{"architecture":"aarch64","image_type":"jdk","os":"alpine-linux",
   "package":{"checksum":"abc123","link":"https://x/OpenJDK21U-jdk_aarch64_alpine-linux_hotspot_21.0.11_10.tar.gz","name":"OpenJDK21U-jdk_aarch64_alpine-linux.tar.gz"}},
   "version":{"semver":"21.0.11+10"}},
  {"binary":{"architecture":"aarch64","image_type":"jre","os":"alpine-linux",
   "package":{"checksum":"def","link":"https://x/jre.tar.gz","name":"jre.tar.gz"}},
   "version":{"semver":"21.0.11+10"}}
]`

func TestSelectAsset(t *testing.T) {
	var assets []adoptiumAsset
	if err := json.Unmarshal([]byte(adoptiumSample), &assets); err != nil {
		t.Fatal(err)
	}

	r, err := selectAsset(assets, "aarch64", "alpine-linux")
	if err != nil {
		t.Fatalf("selectAsset: %v", err)
	}
	if r.FullVersion != "21.0.11+10" || r.SHA256 != "abc123" {
		t.Errorf("selected %+v, want the jdk aarch64 alpine asset", r)
	}
	if !json.Valid([]byte(adoptiumSample)) || r.URL == "" {
		t.Errorf("missing url: %+v", r)
	}

	// Wrong platform → no match.
	if _, err := selectAsset(assets, "x64", "linux"); err == nil {
		t.Error("selectAsset should fail when no asset matches the platform")
	}

	// A jdk asset with no checksum is rejected.
	noSum := []adoptiumAsset{{}}
	noSum[0].Binary.ImageType = "jdk"
	noSum[0].Binary.Architecture = "x64"
	noSum[0].Binary.OS = "linux"
	noSum[0].Binary.Package.Link = "https://x/jdk.tar.gz"
	if _, err := selectAsset(noSum, "x64", "linux"); err == nil {
		t.Error("selectAsset should reject an asset without a checksum")
	}
}

func TestVersionMatches(t *testing.T) {
	cases := map[[2]string]bool{
		{"21.0.11+10", "21"}:      true,
		{"21.0.11+10", "21.0"}:    true,
		{"21.0.11+10", "21.0.11"}: true,
		{"21.0.11+10", "2"}:       false, // not a prefix-on-dot
		{"21.0.11+10", "17"}:      false,
		{"2.0.0", "21"}:           false,
		{"21.0.11+10", ""}:        true, // empty matches any
	}
	for in, want := range cases {
		if got := versionMatches(in[0], in[1]); got != want {
			t.Errorf("versionMatches(%q,%q) = %v, want %v", in[0], in[1], got, want)
		}
	}
}

func TestParseVerAndLess(t *testing.T) {
	if got := parseVer("21.0.11+10"); got != [3]int{21, 0, 11} {
		t.Errorf("parseVer(21.0.11+10) = %v, want {21 0 11}", got)
	}
	if got := parseVer("17"); got != [3]int{17, 0, 0} {
		t.Errorf("parseVer(17) = %v, want {17 0 0}", got)
	}
	if !lessVer([3]int{21, 0, 9}, [3]int{21, 0, 11}) {
		t.Error("21.0.9 should be < 21.0.11")
	}
	if lessVer([3]int{21, 0, 11}, [3]int{21, 0, 11}) {
		t.Error("equal versions are not less")
	}
}
