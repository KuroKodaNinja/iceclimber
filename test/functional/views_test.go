//go:build functional

package functional

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The `logs` and `tui` views read controller-local files (the activity JSONL the serve
// loop writes + the bridged agent.log) and do not open an SSH session, so these drive
// the real binary against fixture files — no VM required.

func writeViewConfig(t *testing.T, activityLog string) string {
	t.Helper()
	content := fmt.Sprintf(`sandbox_id: viewtest
ssh:
  host: 127.0.0.1
  user: nobody
remote_root: /tmp/viewtest
activity_log: %s
`, activityLog)
	p := filepath.Join(t.TempDir(), "iceclimber.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLogsRendersBothStreams: `iceclimber logs` (one-shot, no --follow) merges Popo's
// activity ([POPO]) with the bridged agent stream ([NANA]) and exits.
func TestLogsRendersBothStreams(t *testing.T) {
	dir := t.TempDir()
	activity := filepath.Join(dir, "activity.jsonl")
	agentLog := filepath.Join(dir, "agent.log")
	if err := os.WriteFile(activity, []byte(`{"ts":"2026-06-28T12:00:00Z","kind":"serviced","type":"python.install","status":"ok","detail":"python 3.12.13"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentLog, []byte("→ Bash: popo python.install 3.12\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := writeViewConfig(t, activity)

	out, err := exec.Command(iceclimberBin, "logs", "--config", cfg, "--agent-log", agentLog).CombinedOutput()
	if err != nil {
		t.Fatalf("logs: %v\n%s", err, out)
	}
	s := string(out)
	if !strings.Contains(s, "[POPO]") || !strings.Contains(s, "python.install") {
		t.Errorf("logs missing [POPO] activity:\n%s", s)
	}
	if !strings.Contains(s, "[NANA]") || !strings.Contains(s, "popo python.install 3.12") {
		t.Errorf("logs missing [NANA] agent stream:\n%s", s)
	}
}

// TestTuiSnapshot: `iceclimber tui --snapshot` renders one dashboard frame headlessly.
func TestTuiSnapshot(t *testing.T) {
	dir := t.TempDir()
	activity := filepath.Join(dir, "activity.jsonl")
	agentLog := filepath.Join(dir, "agent.log")
	if err := os.WriteFile(activity, []byte(`{"ts":"2026-06-28T12:00:00Z","kind":"serviced","type":"node.install","status":"ok","detail":"node 24"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentLog, []byte("→ Bash: popo node.install 24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := writeViewConfig(t, activity)

	out, err := exec.Command(iceclimberBin, "tui", "--snapshot", "--config", cfg, "--agent-log", agentLog).CombinedOutput()
	if err != nil {
		t.Fatalf("tui --snapshot: %v\n%s", err, out)
	}
	s := string(out)
	for _, want := range []string{"viewtest", "[POPO]", "[NANA]"} {
		if !strings.Contains(s, want) {
			t.Errorf("tui snapshot missing %q:\n%s", want, s)
		}
	}
}
