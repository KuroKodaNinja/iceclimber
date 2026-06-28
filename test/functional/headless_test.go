//go:build functional

package functional

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestHeadlessBareConsole confirms the command-line mode keeps working with the TUI
// present: bare `iceclimber` in a non-terminal environment (piped stdin/stdout, as
// in CI) must NOT try to launch the Bubble Tea console — it falls back to the
// unattended serve loop. We run it with a short deadline and assert the headless
// banner appears before killing it.
func TestHeadlessBareConsole(t *testing.T) {
	sb := requireSandbox(t)
	cfg := writeConfig(t, sb)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, iceclimberBin, "--config", cfg, "--transport", "sftp")
	cmd.Stdin = strings.NewReader("") // not a terminal
	cmd.Stdout = &out
	cmd.Stderr = &out
	_ = cmd.Run() // killed by the deadline; we only assert the early output

	got := out.String()
	if !strings.Contains(got, "no terminal detected") {
		t.Errorf("bare iceclimber in a non-tty must fall back to headless serve; output:\n%s", got)
	}
	if !strings.Contains(got, "(headless)") {
		t.Errorf("expected the headless serve banner; output:\n%s", got)
	}
}
