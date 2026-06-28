package node

import "testing"

func TestNodePlatform(t *testing.T) {
	tests := []struct {
		os, arch, libc string
		wantBase       string
		wantTag        string
		wantErr        bool
	}{
		{"linux", "x86_64", "glibc", distBaseGlibc, "linux-x64", false},
		{"linux", "aarch64", "glibc", distBaseGlibc, "linux-arm64", false},
		{"linux", "x86_64", "musl", distBaseMusl, "linux-x64-musl", false},
		{"linux", "aarch64", "musl", distBaseMusl, "linux-arm64-musl", false},
		{"darwin", "x86_64", "glibc", "", "", true},
		{"linux", "riscv64", "glibc", "", "", true},
		{"linux", "x86_64", "uclibc", "", "", true},
	}
	for _, tt := range tests {
		base, tag, err := nodePlatform(tt.os, tt.arch, tt.libc)
		if tt.wantErr {
			if err == nil {
				t.Errorf("nodePlatform(%s,%s,%s): want error", tt.os, tt.arch, tt.libc)
			}
			continue
		}
		if err != nil || base != tt.wantBase || tag != tt.wantTag {
			t.Errorf("nodePlatform(%s,%s,%s) = %q,%q,%v; want %q,%q", tt.os, tt.arch, tt.libc, base, tag, err, tt.wantBase, tt.wantTag)
		}
	}
}

func TestPickVersion(t *testing.T) {
	idx := []indexEntry{
		{Version: "v22.3.0", Files: []string{"linux-x64"}},
		{Version: "v20.11.1", Files: []string{"linux-x64", "linux-arm64-musl"}},
		{Version: "v20.10.0", Files: []string{"linux-x64"}},
		{Version: "v18.19.0", Files: []string{"linux-x64", "linux-x64-musl"}},
	}
	tests := []struct {
		requested, fileTag string
		want               string
		wantErr            bool
	}{
		{"20", "linux-x64", "v20.11.1", false},        // highest 20.x with glibc x64
		{"20", "linux-arm64-musl", "v20.11.1", false}, // only 20.11.1 ships arm64-musl
		{"20.10", "linux-x64", "v20.10.0", false},     // pinned minor
		{"18", "linux-x64-musl", "v18.19.0", false},   // musl x64
		{"18", "linux-arm64-musl", "", true},          // no arm64-musl for 18 → clear error
		{"99", "linux-x64", "", true},                 // no such line
	}
	for _, tt := range tests {
		got, err := pickVersion(idx, tt.requested, tt.fileTag)
		if tt.wantErr {
			if err == nil {
				t.Errorf("pickVersion(%q,%q): want error, got %q", tt.requested, tt.fileTag, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("pickVersion(%q,%q) = %q,%v; want %q", tt.requested, tt.fileTag, got, err, tt.want)
		}
	}
}

func TestVersionMatches(t *testing.T) {
	cases := map[[2]string]bool{
		{"20.11.1", "20"}:      true,
		{"20.11.1", "20.11"}:   true,
		{"20.11.1", "20.11.1"}: true,
		{"20.11.1", "2"}:       false, // not a prefix-on-dot
		{"20.11.1", "21"}:      false,
		{"2.0.0", "20"}:        false,
	}
	for in, want := range cases {
		if got := versionMatches(in[0], in[1]); got != want {
			t.Errorf("versionMatches(%q,%q) = %v, want %v", in[0], in[1], got, want)
		}
	}
}
