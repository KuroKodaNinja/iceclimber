package cli

import (
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/shell"
)

// TestInteractiveLoginCommand: the PTY login script prefers the installed bash (with its rc),
// falls back to /bin/sh with the env sourced + a hint, and both branches `exec` (so the remote
// shell's exit is the session's exit).
func TestInteractiveLoginCommand(t *testing.T) {
	root := "/home/agent/.iceclimber"
	got := interactiveLoginCommand(root)
	for _, want := range []string{
		"if [ -x '" + shell.BinPath(root) + "' ]; then",
		"exec '" + shell.BinPath(root) + "' --rcfile '" + shell.RCPath(root) + "' -i",
		"else",
		shell.EnvPreamble(root), // the no-bash branch sources the same env
		"no bash installed",     // actionable hint
		"exec /bin/sh",
		"fi",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("login command missing %q:\n%s", want, got)
		}
	}
	// Every exec must actually replace the shell (no lingering parent), so both branches exec.
	if strings.Count(got, "exec ") != 2 {
		t.Errorf("expected exactly two exec branches:\n%s", got)
	}
}
