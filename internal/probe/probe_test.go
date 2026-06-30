package probe

import (
	"context"
	"io"
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

func TestDetectRuntimes(t *testing.T) {
	t.Run("python with venv+conda, node, java", func(t *testing.T) {
		kv := map[string]string{
			"PY_PATH": "/usr/bin/python3", "PY_VER": "Python 3.11.2",
			"PY_VENV": "yes", "PY_ENSUREPIP": "yes", "CONDA_PATH": "/opt/conda/bin/conda",
			"NODE_PATH": "/usr/bin/node", "NODE_VER": "v20.1.0",
			"JAVA_PATH": "/usr/bin/java", "JAVA_VER": `openjdk version "17.0.1" 2021-10-19`,
		}
		rts := detectRuntimes(kv)
		if len(rts) != 3 {
			t.Fatalf("got %d runtimes, want 3: %+v", len(rts), rts)
		}
		py := rts[0]
		if py.Lang != "python" || py.Path != "/usr/bin/python3" || py.Version != "3.11.2" {
			t.Errorf("python = %+v", py)
		}
		if strings.Join(py.EnvManagers, ",") != "venv,conda" {
			t.Errorf("python env managers = %v, want [venv conda]", py.EnvManagers)
		}
		if rts[1].Version != "20.1.0" {
			t.Errorf("node version = %q, want 20.1.0", rts[1].Version)
		}
		if rts[2].Version != "17.0.1" {
			t.Errorf("java version = %q, want 17.0.1", rts[2].Version)
		}
	})

	t.Run("python without a working venv lists no venv manager", func(t *testing.T) {
		// Debian ships python3 where `import venv` works but ensurepip is missing.
		kv := map[string]string{"PY_PATH": "/usr/bin/python3", "PY_VER": "Python 3.12.0", "PY_VENV": "yes"}
		rts := detectRuntimes(kv)
		if len(rts) != 1 || len(rts[0].EnvManagers) != 0 {
			t.Errorf("want python with no env managers (ensurepip missing), got %+v", rts)
		}
	})

	t.Run("nothing on PATH", func(t *testing.T) {
		if rts := detectRuntimes(map[string]string{}); len(rts) != 0 {
			t.Errorf("want no runtimes, got %+v", rts)
		}
	})

	t.Run("present but unparseable version", func(t *testing.T) {
		rts := detectRuntimes(map[string]string{"PY_PATH": "/usr/bin/python3", "PY_VER": "garbage"})
		if len(rts) != 1 || rts[0].Path != "/usr/bin/python3" || rts[0].Version != "" {
			t.Errorf("want python with empty Version (raw kept), got %+v", rts)
		}
		if rts[0].VersionRaw != "garbage" {
			t.Errorf("VersionRaw should keep the raw line, got %q", rts[0].VersionRaw)
		}
	})
}

func TestParseRuntimeVersions(t *testing.T) {
	if got := parsePythonVersion("Python 3.11.2"); got != "3.11.2" {
		t.Errorf("python = %q", got)
	}
	if got := parseNodeVersion("v20.1.0"); got != "20.1.0" {
		t.Errorf("node = %q", got)
	}
	if got := parseJavaVersion(`java version "1.8.0_292"`); got != "1.8.0_292" {
		t.Errorf("java (legacy) = %q", got)
	}
	if got := parseJavaVersion(`openjdk version "17.0.1" 2021-10-19`); got != "17.0.1" {
		t.Errorf("java (modern) = %q", got)
	}
	if got := parseJavaVersion("garbage"); got != "" {
		t.Errorf("java (unparseable) = %q, want empty", got)
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

// fakeRunner routes commands to canned outputs by matching on their content,
// mocking at the remote.Runner boundary.
type fakeRunner struct {
	system   string
	writable map[string]bool // root path -> whether the write test "succeeds"
	tree     bool
}

func (f *fakeRunner) Run(_ context.Context, cmd string, _ io.Reader) (remote.Result, error) {
	switch {
	case strings.Contains(cmd, "uname -s"):
		return remote.Result{Stdout: []byte(f.system)}, nil
	case strings.Contains(cmd, ".iceclimber-writetest"):
		for path, ok := range f.writable {
			if strings.Contains(cmd, remote.ShellQuote(path)) {
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

func TestRun_DiscoversSystemRuntimes(t *testing.T) {
	fr := &fakeRunner{
		system: "OS=Linux\nARCH=x86_64\nHOME=/home/agent\nGLIBC=glibc 2.31\n" +
			"PY_PATH=/usr/bin/python3\nPY_VER=Python 3.11.2\nPY_VENV=yes\nPY_ENSUREPIP=yes\n" +
			"NODE_PATH=/usr/bin/node\nNODE_VER=v20.1.0\n",
		writable: map[string]bool{"/home/agent/.iceclimber": true},
	}
	fp, err := Run(context.Background(), fr, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	py, ok := fp.Runtime("python")
	if !ok || py.Version != "3.11.2" || len(py.EnvManagers) != 1 || py.EnvManagers[0] != "venv" {
		t.Errorf("python runtime = %+v (ok=%v)", py, ok)
	}
	if _, ok := fp.Runtime("node"); !ok {
		t.Error("node runtime not discovered")
	}
	if _, ok := fp.Runtime("java"); ok {
		t.Error("java should be absent (no JAVA_PATH key)")
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
