package cli

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/probe"
	"github.com/KuroKodaNinja/iceclimber/internal/runtimes"
)

// TestPromptRuntimeChoice pins the cmdline prompt's input mapping: "system"/"s"
// chooses the system runtime; anything else (incl. empty/Enter) keeps managed.
func TestPromptRuntimeChoice(t *testing.T) {
	fp := &probe.Fingerprint{Runtimes: []probe.RuntimeInfo{
		{Lang: "python", Path: "/usr/bin/python3", Version: "3.12.1"},
	}}
	cases := map[string]runtimes.Mode{
		"system\n":   runtimes.ModeSystem,
		"s\n":        runtimes.ModeSystem,
		"managed\n":  runtimes.ModeManaged,
		"\n":         runtimes.ModeManaged, // bare Enter → default managed
		"nonsense\n": runtimes.ModeManaged,
	}
	for in, want := range cases {
		var out bytes.Buffer
		got := promptRuntimeChoice(&out, bufio.NewReader(strings.NewReader(in)), "python", fp)
		if got.Mode != want {
			t.Errorf("input %q → %q, want %q", strings.TrimSpace(in), got.Mode, want)
		}
		if !strings.Contains(out.String(), "3.12.1") {
			t.Errorf("prompt should mention the detected version; got %q", out.String())
		}
	}
}
