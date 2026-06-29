package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/agent"
)

func TestParseTokenFile(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{"export form", "export CLAUDE_CODE_OAUTH_TOKEN=tok123\n", "tok123"},
		{"bare assignment", "CLAUDE_CODE_OAUTH_TOKEN=tok123", "tok123"},
		{"double quoted", `export CLAUDE_CODE_OAUTH_TOKEN="tok123"`, "tok123"},
		{"single quoted", "export CLAUDE_CODE_OAUTH_TOKEN='tok123'", "tok123"},
		{"bare token", "tok123\n", "tok123"},
		{"comment then bare", "# my token\ntok123\n", "tok123"},
		{"ignores other vars", "export OTHER=nope\nexport CLAUDE_CODE_OAUTH_TOKEN=yes\n", "yes"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseTokenFile(c.content, "CLAUDE_CODE_OAUTH_TOKEN"); got != c.want {
				t.Errorf("parseTokenFile(%q) = %q, want %q", c.content, got, c.want)
			}
		})
	}
}

func TestResolveAgentToken_RejectsAPIKey(t *testing.T) {
	f := filepath.Join(t.TempDir(), "tok.env")
	if err := os.WriteFile(f, []byte("export CLAUDE_CODE_OAUTH_TOKEN=sk-ant-api-xyz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAgentToken(agent.Claude, f); err == nil {
		t.Error("API-key token was accepted; want rejection")
	}
}

func TestResolveAgentToken_Missing(t *testing.T) {
	f := filepath.Join(t.TempDir(), "empty.env")
	if err := os.WriteFile(f, []byte("# nothing here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAgentToken(agent.Claude, f); err == nil {
		t.Error("empty token file accepted; want error")
	}
}
