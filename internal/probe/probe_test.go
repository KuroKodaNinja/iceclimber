package probe

import (
	"context"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

func TestDetectLibc(t *testing.T) {
	tests := []struct {
		name           string
		os             string
		kv             map[string]string
		wantFamily     string
		wantConfidence bool
		wantVersion    string
	}{
		{
			name:           "glibc via getconf",
			os:             "linux",
			kv:             map[string]string{"GLIBC": "glibc 2.31", "LDD": "ldd (Ubuntu GLIBC 2.31-0ubuntu9) 2.31"},
			wantFamily:     "glibc",
			wantConfidence: true,
			wantVersion:    "2.31",
		},
		{
			name:           "musl via ld-musl path",
			os:             "linux",
			kv:             map[string]string{"MUSL": "/lib/ld-musl-x86_64.so.1", "LDD": "musl libc (x86_64)"},
			wantFamily:     "musl",
			wantConfidence: true,
		},
		{
			name:           "conflicting signals are low-confidence",
			os:             "linux",
			kv:             map[string]string{"MUSL": "/lib/ld-musl-x86_64.so.1", "GLIBC": "glibc 2.31"},
			wantFamily:     "unknown",
			wantConfidence: false,
		},
		{
			name:           "no signal on linux is low-confidence",
			os:             "linux",
			kv:             map[string]string{},
			wantFamily:     "unknown",
			wantConfidence: false,
		},
		{
			name:           "non-linux is n/a",
			os:             "darwin",
			kv:             map[string]string{},
			wantFamily:     "n/a",
			wantConfidence: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectLibc(tt.os, tt.kv)
			if got.Family != tt.wantFamily {
				t.Errorf("Family = %q, want %q", got.Family, tt.wantFamily)
			}
			if got.HighConfidence != tt.wantConfidence {
				t.Errorf("HighConfidence = %v, want %v", got.HighConfidence, tt.wantConfidence)
			}
			if tt.wantVersion != "" && got.Version != tt.wantVersion {
				t.Errorf("Version = %q, want %q", got.Version, tt.wantVersion)
			}
		})
	}
}

func TestParseDFAvail(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"/dev/sda1 41251136 12345678 26789012 32% /home", 26789012},
		{"tmpfs 1024000 0 1024000 0% /tmp", 1024000},
		{"too few fields", 0},
		{"", 0},
		{"a b c notanumber e f", 0},
	}
	for _, tt := range tests {
		if got := parseDFAvail(tt.in); got != tt.want {
			t.Errorf("parseDFAvail(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseKV(t *testing.T) {
	out := "OS=Linux\nARCH=x86_64\nDF=/dev/sda1 100 50 50 50% /\nEMPTY=\nnoequals\n"
	kv := parseKV(out)
	if kv["OS"] != "Linux" {
		t.Errorf("OS = %q, want Linux", kv["OS"])
	}
	if kv["DF"] != "/dev/sda1 100 50 50 50% /" {
		t.Errorf("DF = %q", kv["DF"])
	}
	if v, ok := kv["EMPTY"]; !ok || v != "" {
		t.Errorf("EMPTY = %q, ok=%v; want empty present", v, ok)
	}
	if _, ok := kv["noequals"]; ok {
		t.Error("line without '=' should be skipped")
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"/home/agent/.iceclimber", `'/home/agent/.iceclimber'`},
		{"/has 'quote'", `'/has '\''quote'\'''`},
	}
	for _, tt := range tests {
		if got := shellQuote(tt.in); got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// fakeRunner routes commands to canned outputs by matching on their content,
// mocking at the remote.Runner boundary.
type fakeRunner struct {
	system   string
	writable map[string]bool // root path -> whether the write test "succeeds"
	tree     bool
}

func (f *fakeRunner) Run(_ context.Context, cmd string) (remote.Result, error) {
	switch {
	case strings.Contains(cmd, "uname -s"):
		return remote.Result{Stdout: []byte(f.system)}, nil
	case strings.Contains(cmd, ".iceclimber-writetest"):
		for path, ok := range f.writable {
			if strings.Contains(cmd, shellQuote(path)) {
				write := "fail"
				if ok {
					write = "ok"
				}
				return remote.Result{Stdout: []byte("MKDIR=ok\nDF=/dev/sda1 100 20 80 20% /\nWRITE=" + write + "\n")}, nil
			}
		}
		return remote.Result{Stdout: []byte("MKDIR=fail\n")}, nil
	case strings.Contains(cmd, "/protocol ]"):
		exists := "no"
		if f.tree {
			exists = "yes"
		}
		return remote.Result{Stdout: []byte("EXISTS=" + exists + "\n")}, nil
	}
	return remote.Result{}, nil
}

func (f *fakeRunner) Close() error { return nil }

func TestRun_GlibcLinuxHappyPath(t *testing.T) {
	fr := &fakeRunner{
		system:   "OS=Linux\nARCH=x86_64\nHOME=/home/agent\nLDD=ldd (GNU libc) 2.31\nGLIBC=glibc 2.31\n",
		writable: map[string]bool{"/home/agent/.iceclimber": true, "/opt/iceclimber": false},
		tree:     false,
	}
	fp, err := Run(context.Background(), fr, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fp.OS != "linux" || fp.Arch != "x86_64" {
		t.Errorf("os/arch = %q/%q", fp.OS, fp.Arch)
	}
	if fp.Libc.Family != "glibc" || !fp.Libc.HighConfidence {
		t.Errorf("libc = %+v", fp.Libc)
	}
	if got := fp.FirstViableRoot(); got != "/home/agent/.iceclimber" {
		t.Errorf("FirstViableRoot = %q, want /home/agent/.iceclimber", got)
	}
	if fp.HasExistingTree {
		t.Error("HasExistingTree = true, want false")
	}
	if len(fp.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", fp.Warnings)
	}
}

func TestRun_NoWritableRootWarns(t *testing.T) {
	fr := &fakeRunner{
		system:   "OS=Linux\nARCH=aarch64\nHOME=/home/agent\nMUSL=/lib/ld-musl-aarch64.so.1\n",
		writable: map[string]bool{}, // nothing writable
	}
	fp, err := Run(context.Background(), fr, Options{ExplicitRoots: []string{"/srv/ic"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fp.FirstViableRoot() != "" {
		t.Errorf("FirstViableRoot = %q, want empty", fp.FirstViableRoot())
	}
	if len(fp.Warnings) == 0 {
		t.Error("expected a no-writable-root warning")
	}
	// explicit root must be tested first
	if len(fp.Roots) == 0 || fp.Roots[0].Path != "/srv/ic" {
		t.Errorf("first candidate = %v, want /srv/ic", fp.Roots)
	}
}
