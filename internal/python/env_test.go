package python

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

// envFakeRunner answers the shell commands EnsureEnv issues in system mode, matching
// on content. venvExists toggles whether the venv interpreter is already present.
type envFakeRunner struct {
	minor      string // system python minor, e.g. "3.12"
	venvExists bool
	created    bool // set when `-m venv` ran
}

func (f *envFakeRunner) Run(_ context.Context, cmd string, _ io.Reader) (remote.Result, error) {
	switch {
	case strings.Contains(cmd, "version_info"):
		return remote.Result{Stdout: []byte(f.minor + "\n")}, nil
	case strings.Contains(cmd, "[ -x "):
		if f.venvExists {
			return remote.Result{Stdout: []byte("ok\n")}, nil
		}
		return remote.Result{Stdout: []byte("\n")}, nil
	case strings.Contains(cmd, "-m venv"):
		f.created = true
		f.venvExists = true
		return remote.Result{}, nil
	case strings.Contains(cmd, "--version"):
		return remote.Result{Stdout: []byte("Python " + f.minor + ".0\n")}, nil
	}
	return remote.Result{}, nil
}
func (f *envFakeRunner) Close() error { return nil }

func TestMinorOf(t *testing.T) {
	for in, want := range map[string]string{"3.12.3": "3.12", "3.12": "3.12", "3": "3", "": ""} {
		if got := minorOf(in); got != want {
			t.Errorf("minorOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnsureEnv_SystemCreatesVenv(t *testing.T) {
	fr := &envFakeRunner{minor: "3.12"}
	bin, err := EnsureEnv(context.Background(), nil, fr, "/root", "3.12", "x86_64", "glibc",
		EnvSpec{Mode: "system", SystemPath: "/usr/bin/python3"})
	if err != nil {
		t.Fatalf("EnsureEnv: %v", err)
	}
	if bin != "/root/envs/python-3.12/bin/python" {
		t.Errorf("bin = %q, want the venv interpreter", bin)
	}
	if !fr.created {
		t.Error("venv was not created")
	}
}

func TestEnsureEnv_SystemReusesVenv(t *testing.T) {
	fr := &envFakeRunner{minor: "3.12", venvExists: true}
	bin, err := EnsureEnv(context.Background(), nil, fr, "/root", "", "x86_64", "glibc",
		EnvSpec{Mode: "system"})
	if err != nil {
		t.Fatalf("EnsureEnv: %v", err)
	}
	if bin != "/root/envs/python-3.12/bin/python" {
		t.Errorf("bin = %q", bin)
	}
	if fr.created {
		t.Error("an existing venv must be reused, not recreated")
	}
}

func TestEnsureEnv_VersionMismatchFailsClearly(t *testing.T) {
	fr := &envFakeRunner{minor: "3.12"}
	_, err := EnsureEnv(context.Background(), nil, fr, "/root", "3.11", "x86_64", "glibc",
		EnvSpec{Mode: "system"})
	if err == nil || !strings.Contains(err.Error(), "3.12") {
		t.Fatalf("want a clear version-mismatch error, got %v", err)
	}
}

func TestEnsureEnv_RejectsUnsupportedManager(t *testing.T) {
	fr := &envFakeRunner{minor: "3.12"}
	_, err := EnsureEnv(context.Background(), nil, fr, "/root", "3.12", "x86_64", "glibc",
		EnvSpec{Mode: "system", EnvManager: "conda"})
	if err == nil || !strings.Contains(err.Error(), "venv") {
		t.Fatalf("conda not supported yet should error pointing at venv, got %v", err)
	}
}
