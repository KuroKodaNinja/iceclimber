package pip

import (
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
)

func TestHasIndex(t *testing.T) {
	if (&Manager{cfg: Config{}}).hasIndex() {
		t.Error("no config index and no extra args → no index")
	}
	if !(&Manager{cfg: Config{IndexURL: "https://m"}}).hasIndex() {
		t.Error("config index → has index")
	}
	if !(&Manager{cfg: Config{ExtraArgs: []string{"--index-url", "https://x"}}}).hasIndex() {
		t.Error("extra_args --index-url → has index even without config")
	}
}

func TestResolveCmdIncludesExtraArgs(t *testing.T) {
	m := &Manager{cfg: Config{
		PythonBin: "/v/bin/python",
		ExtraArgs: []string{"--index-url", "https://download.pytorch.org/whl/cpu", "--pre"},
	}}
	cmd := m.resolveCmd([]pkg.Spec{{Name: "torch"}}, "/state/report.json")
	for _, want := range []string{
		"-m pip install", "--dry-run",
		"'--index-url' 'https://download.pytorch.org/whl/cpu'", "'--pre'",
		"'torch'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("resolveCmd missing %q in:\n%s", want, cmd)
		}
	}
}

// With no config index, indexArgs emits no --index-url (so the agent's extra_args
// one is the only index — and isn't shadowed by an empty config value).
func TestIndexArgsOmittedWhenUnset(t *testing.T) {
	if got := (&Manager{cfg: Config{}}).indexArgs(); len(got) != 0 {
		t.Errorf("indexArgs with no config = %v, want empty", got)
	}
}
