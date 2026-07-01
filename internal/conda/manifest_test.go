package conda

import (
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
	}
	for in, want := range cases {
		if got := parseCondaDep(in); got != want {
			t.Errorf("parseCondaDep(%q) = %+v, want %+v", in, got, want)
		}
	}
}
