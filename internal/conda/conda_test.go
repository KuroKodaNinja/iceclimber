package conda

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

type fakeRunner struct {
	lastCmd string
	stdout  string
	exit    int
}

func (f *fakeRunner) Run(_ context.Context, cmd string, _ io.Reader) (remote.Result, error) {
	f.lastCmd = cmd
	return remote.Result{Stdout: []byte(f.stdout), ExitCode: f.exit}, nil
}
func (f *fakeRunner) Close() error { return nil }

func TestInstallCmd(t *testing.T) {
	m := New(Config{
		CondaBin: "/opt/conda/bin/conda", EnvPrefix: "/root/envs/conda-python-3.12",
		ExtraArgs: []string{"-c", "conda-forge"},
	})
	cmd := m.installCmd([]pkg.Spec{{Name: "numpy", Version: "1.26"}, {Name: "six"}})
	for _, want := range []string{"install", "-y", "--json", "-p", "/root/envs/conda-python-3.12", "conda-forge", "numpy=1.26", "six"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("installCmd missing %q:\n%s", want, cmd)
		}
	}
	// conda uses a single '=' match-spec, not pip's '=='.
	if strings.Contains(cmd, "numpy==1.26") {
		t.Errorf("conda spec should use single '=': %s", cmd)
	}
}

func TestInstall_Success(t *testing.T) {
	fr := &fakeRunner{stdout: `{"success":true,"actions":{"LINK":[{"name":"numpy","version":"1.26.4"},{"name":"libopenblas","version":"0.3"}]}}`}
	m := New(Config{Runner: fr, CondaBin: "conda", EnvPrefix: "/e"})
	out, err := m.Install(context.Background(), []pkg.Spec{{Name: "numpy"}})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(out.Failed) != 0 || len(out.Installed) != 1 {
		t.Fatalf("outcome = %+v, want 1 installed / 0 failed", out)
	}
	if out.Installed[0].Name != "numpy" || out.Installed[0].Version != "1.26.4" {
		t.Errorf("installed = %+v, want numpy 1.26.4 (version from LINK)", out.Installed[0])
	}
}

func TestInstall_Failure(t *testing.T) {
	fr := &fakeRunner{stdout: `{"success":false,"error":"PackagesNotFoundError: nonesuch"}`, exit: 1}
	m := New(Config{Runner: fr, CondaBin: "conda", EnvPrefix: "/e"})
	out, err := m.Install(context.Background(), []pkg.Spec{{Name: "nonesuch"}})
	if err != nil {
		t.Fatalf("a solve failure is a per-spec Failed, not a hard error: %v", err)
	}
	if len(out.Installed) != 0 || len(out.Failed) != 1 {
		t.Fatalf("outcome = %+v, want 0 installed / 1 failed", out)
	}
	if !strings.Contains(out.Failed[0].Error, "PackagesNotFoundError") {
		t.Errorf("failure should carry the conda error: %+v", out.Failed[0])
	}
}

func TestParseCondaJSON_ToleratesLeadingNoise(t *testing.T) {
	cr, err := parseCondaJSON([]byte("Collecting package metadata...\n{\"success\":true}\n"))
	if err != nil || !cr.Success {
		t.Errorf("parseCondaJSON = %+v, %v; want success", cr, err)
	}
}

func TestExtraArgAllow(t *testing.T) {
	ok := [][]string{{"-c", "conda-forge"}, {"--channel", "bioconda", "--offline"}, {"--override-channels"}, {"--use-local"}}
	for _, a := range ok {
		if err := pkg.ValidateExtraArgs(a, extraArgAllow); err != nil {
			t.Errorf("ValidateExtraArgs(%v) = %v, want ok", a, err)
		}
	}
	bad := [][]string{{"numpy"}, {"--index-url", "x"}, {"-c"}, {"--dev"}}
	for _, a := range bad {
		if err := pkg.ValidateExtraArgs(a, extraArgAllow); err == nil {
			t.Errorf("ValidateExtraArgs(%v) = nil, want rejected", a)
		}
	}
}
