//go:build functional

package functional

import (
	"fmt"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestSkillAndStatus verifies the v1 cap-stone on the VM: bootstrap drops a
// readable NANA.md matching `skill print`, and `status` reports liveness, queue,
// the installed runtime, and the agent's self-reported capabilities.
func TestSkillAndStatus(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-skill-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	// NANA.md was dropped and matches `skill print`.
	printed := string(runIceclimber(t, "skill", "print"))
	if len(printed) < 1000 || !strings.Contains(printed, "NANA.md") {
		t.Fatalf("skill print looks wrong (%d bytes)", len(printed))
	}
	inSandbox := limaSh(t, fmt.Sprintf("cat %s/skill/NANA.md", root))
	if strings.TrimSpace(inSandbox) != strings.TrimSpace(printed) {
		t.Errorf("dropped NANA.md differs from skill print (%d vs %d bytes)", len(inSandbox), len(printed))
	}

	// Install a runtime, write capabilities by hand, stamp the heartbeat.
	runIceclimber(t, "install", "python", "3.12", "--config", cfg, "--transport", "sftp")
	limaSh(t, fmt.Sprintf(`printf '%%s' '{"has_exec":true,"has_file_write":true}' > %s/protocol/capabilities.json`, root))
	runIceclimber(t, "serve", "--once", "--config", cfg, "--transport", "sftp")

	out := string(runIceclimber(t, "status", "--config", cfg, "--transport", "sftp"))
	for _, want := range []string{"heartbeat: seq", "python:    3.12", "has_exec=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
}
