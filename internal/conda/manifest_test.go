package conda

import (
	"context"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
)

func TestParseEnvironment(t *testing.T) {
	yml := []byte(`name: mlkit
channels:
  - conda-forge
  - bioconda
dependencies:
  - python=3.12
  - pytorch
  - pandas=2.2
  - numpy 1.26
`)
	name, chArgs, py, specs, err := parseEnvironment(yml)
	if err != nil {
		t.Fatalf("parseEnvironment: %v", err)
	}
	if name != "mlkit" {
		t.Errorf("name = %q, want mlkit", name)
	}
	if strings.Join(chArgs, " ") != "-c conda-forge -c bioconda" {
		t.Errorf("channelArgs = %v", chArgs)
	}
	if py != "3.12" {
		t.Errorf("python = %q, want 3.12", py)
	}
	// python is extracted (not a spec); the rest are conda specs with parsed versions.
	want := map[string]string{"pytorch": "", "pandas": "2.2", "numpy": "1.26"}
	if len(specs) != 3 {
		t.Fatalf("specs = %+v, want 3", specs)
	}
	for _, s := range specs {
		if w, ok := want[s.Name]; !ok || w != s.Version {
			t.Errorf("spec %+v not in want %v", s, want)
		}
	}
}

func TestParseEnvironment_Errors(t *testing.T) {
	// A pip: subsection is rejected (conda-only for now).
	pipYML := []byte("name: x\ndependencies:\n  - python=3.12\n  - pip:\n      - requests\n")
	if _, _, _, _, err := parseEnvironment(pipYML); err == nil || !strings.Contains(err.Error(), "pip") {
		t.Errorf("pip subsection should be rejected, got %v", err)
	}
	// No python pin is an error (the relay needs a minor).
	noPy := []byte("name: x\ndependencies:\n  - numpy\n")
	if _, _, _, _, err := parseEnvironment(noPy); err == nil {
		t.Error("missing python pin should error")
	}
}

func TestParseCondaDep(t *testing.T) {
	cases := map[string]pkg.Spec{
		"pytorch":        {Name: "pytorch"},
		"python=3.12":    {Name: "python", Version: "3.12"},
		"pandas==2.2":    {Name: "pandas", Version: "2.2"},
		"numpy 1.26":     {Name: "numpy", Version: "1.26"},
		"  spaced=1.0  ": {Name: "spaced", Version: "1.0"},
		// Comparison constraints keep their operator (for condaSpec to render verbatim).
		"numpy>=1.20":  {Name: "numpy", Version: ">=1.20"},
		"numpy >=1.20": {Name: "numpy", Version: ">=1.20"},
		"pkg!=1.5":     {Name: "pkg", Version: "!=1.5"},
	}
	for in, want := range cases {
		if got := parseCondaDep(in); got != want {
			t.Errorf("parseCondaDep(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestCondaSpec_Operators(t *testing.T) {
	cases := map[pkg.Spec]string{
		{Name: "six"}:                      "six",
		{Name: "pandas", Version: "2.2"}:   "pandas=2.2",  // exact pin → single '='
		{Name: "numpy", Version: ">=1.20"}: "numpy>=1.20", // constraint kept verbatim
		{Name: "pkg", Version: "!=1.5"}:    "pkg!=1.5",
	}
	for spec, want := range cases {
		if got := condaSpec(spec); got != want {
			t.Errorf("condaSpec(%+v) = %q, want %q", spec, got, want)
		}
	}
}

func TestCreateEnv(t *testing.T) {
	fr := &fakeRunner{stdout: `{"success":true,"actions":{"LINK":[{"name":"numpy","version":"1.26.4"}]}}`}
	m := New(Config{Runner: fr, CondaBin: "/opt/conda/bin/conda",
		EnvPrefix: "/root/envs/mlkit", ExtraArgs: []string{"-c", "conda-forge"}})
	out, err := m.CreateEnv(context.Background(), "3.12.5", []pkg.Spec{{Name: "numpy", Version: "1.26"}})
	if err != nil {
		t.Fatalf("CreateEnv: %v", err)
	}
	for _, want := range []string{"create", "-y", "--json", "-p", "/root/envs/mlkit", "conda-forge", "python=3.12", "numpy=1.26"} {
		if !strings.Contains(fr.lastCmd, want) {
			t.Errorf("CreateEnv cmd missing %q:\n%s", want, fr.lastCmd)
		}
	}
	if len(out.Installed) != 1 || out.Installed[0].Tier != pkg.TierMirror {
		t.Errorf("outcome = %+v, want numpy tagged mirror", out.Installed)
	}
	// An empty python version is a clear error (env needs a minor).
	if _, err := m.CreateEnv(context.Background(), "", nil); err == nil {
		t.Error("CreateEnv with no python version should error")
	}
}
