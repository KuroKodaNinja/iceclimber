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
	return writeViewConfigAudit(t, activityLog, "")
}

// writeViewConfigAudit is writeViewConfig with an explicit audit_log path (for the
// [EGRESS] feed). An empty auditLog omits the key (audit defaults off in these fixtures).
func writeViewConfigAudit(t *testing.T, activityLog, auditLog string) string {
	t.Helper()
	content := fmt.Sprintf(`sandbox_id: viewtest
ssh:
  host: 127.0.0.1
  user: nobody
remote_root: /tmp/viewtest
activity_log: %s
`, activityLog)
	if auditLog != "" {
		content += "audit_log: " + auditLog + "\n"
	}
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

// TestLogsSurfacesEgressAudit: `iceclimber logs` folds the egress audit trail into the
// merged feed as [EGRESS] lines, and the --only/--kind filters select sources/kinds.
func TestLogsSurfacesEgressAudit(t *testing.T) {
	dir := t.TempDir()
	activity := filepath.Join(dir, "activity.jsonl")
	auditLog := filepath.Join(dir, "audit.jsonl")
	if err := os.WriteFile(activity, []byte(
		`{"ts":"2026-06-28T12:00:00Z","kind":"serviced","type":"web.fetch","status":"ok","detail":"200 example.com"}`+"\n"+
			`{"ts":"2026-06-28T12:00:01Z","kind":"denied","detail":"https://evil/"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(auditLog, []byte(
		`{"ts":"2026-06-28T12:00:00Z","type":"web.fetch","url":"https://example.com/x","method":"GET","venue":"sandbox-exec","decision":"allow","status_code":200,"outcome":"ok"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := writeViewConfigAudit(t, activity, auditLog)

	// Default: [POPO] + [EGRESS] both present.
	out, err := exec.Command(iceclimberBin, "logs", "--config", cfg, "--agent-log", "none").CombinedOutput()
	if err != nil {
		t.Fatalf("logs: %v\n%s", err, out)
	}
	s := string(out)
	if !strings.Contains(s, "[EGRESS]") || !strings.Contains(s, "https://example.com/x") || !strings.Contains(s, "allow") {
		t.Errorf("logs missing [EGRESS] audit line:\n%s", s)
	}
	if !strings.Contains(s, "[POPO]") {
		t.Errorf("logs missing [POPO] activity:\n%s", s)
	}

	// --only egress: [POPO] is suppressed.
	out, err = exec.Command(iceclimberBin, "logs", "--config", cfg, "--agent-log", "none", "--only", "egress").CombinedOutput()
	if err != nil {
		t.Fatalf("logs --only egress: %v\n%s", err, out)
	}
	s = string(out)
	if strings.Contains(s, "[POPO]") || !strings.Contains(s, "[EGRESS]") {
		t.Errorf("--only egress should show only [EGRESS]:\n%s", s)
	}

	// --only popo --kind denied: just the denied [POPO] line, no serviced, no [EGRESS].
	out, err = exec.Command(iceclimberBin, "logs", "--config", cfg, "--agent-log", "none", "--only", "popo", "--kind", "denied").CombinedOutput()
	if err != nil {
		t.Fatalf("logs --kind denied: %v\n%s", err, out)
	}
	s = string(out)
	if strings.Contains(s, "[EGRESS]") || strings.Contains(s, "web.fetch") || !strings.Contains(s, "denied") {
		t.Errorf("--only popo --kind denied should show only the denied [POPO] line:\n%s", s)
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
