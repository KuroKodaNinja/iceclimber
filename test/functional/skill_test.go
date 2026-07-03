//go:build functional

package functional

import (
	"encoding/json"
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

	// Install a runtime and stamp the heartbeat. Bootstrap already wrote the host
	// capabilities block (no hand-written file needed), so status reports the real
	// self-report — "no agent yet" + the host facts — instead of "(not reported)".
	runIceclimber(t, "install", "python", "3.12", "--config", cfg, "--transport", "sftp")
	runIceclimber(t, "serve", "--once", "--config", cfg, "--transport", "sftp")

	out := string(runIceclimber(t, "status", "--config", cfg, "--transport", "sftp"))
	// status lists installed runtimes for all languages under one "runtimes:" line; the
	// agent line shows the bootstrap-written host facts (this is the musl sandbox).
	for _, want := range []string{"heartbeat: seq", "runtimes:  python 3.12", "no agent yet", "musl"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}

	// --json is the machine-readable shape (and the schema the fleet table will
	// aggregate, plan §14.6): parseable, same facts.
	jout := runIceclimber(t, "status", "--json", "--config", cfg, "--transport", "sftp")
	var parsed struct {
		Sandbox   string   `json:"sandbox"`
		Root      string   `json:"root"`
		Heartbeat string   `json:"heartbeat_seq"`
		Runtimes  []string `json:"runtimes"`
	}
	if err := json.Unmarshal(jout, &parsed); err != nil {
		t.Fatalf("status --json unparseable: %v\n%s", err, jout)
	}
	if parsed.Sandbox == "" || parsed.Root == "" || parsed.Heartbeat == "" || len(parsed.Runtimes) == 0 {
		t.Errorf("status --json missing facts: %+v", parsed)
	}
}
