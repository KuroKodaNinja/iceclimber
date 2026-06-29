//go:build functional

package functional

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// startServe runs `iceclimber serve --yes` in the background under a PRIVATE HOME, so
// its controller-side state (activity.jsonl, agent.log) lands in a temp dir instead of
// polluting the operator's real ~/.iceclimber. Returns the cmd (killed at test end) and
// the agent.log path under that HOME. The runtime cache is os.TempDir-based, so a temp
// HOME doesn't force re-downloads.
func startServe(t *testing.T, cfg string) (*exec.Cmd, string) {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command(iceclimberBin, "serve", "--yes", "--config", cfg, "--transport", "sftp")
	cmd.Env = append(os.Environ(), "HOME="+home)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return cmd, filepath.Join(home, ".iceclimber", sandboxName, "agent.log")
}

// TestAgentInstallClaude installs the Claude Code agent into the sandbox via the
// official command: the controller downloads the agent's package for the SANDBOX's
// platform and relays the native binary in (no on-target install — the air-gap
// path), writes a 0600 auth env file, and verifies `claude --version` runs on musl.
// A throwaway token is used — --version does not authenticate, so this exercises the
// whole relay/auth-config/verify path without a real credential.
func TestAgentInstallClaude(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-agent-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	tokenFile := filepath.Join(t.TempDir(), "token.env")
	if err := os.WriteFile(tokenFile, []byte("export CLAUDE_CODE_OAUTH_TOKEN=throwaway-not-a-real-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := string(runIceclimber(t, "agent", "install", "claude",
		"--token-file", tokenFile, "--transport", "sftp", "--config", cfg))
	if !strings.Contains(out, "installed Claude Code (claude)") || !strings.Contains(out, "auth:   configured") {
		t.Errorf("agent install output unexpected:\n%s", out)
	}

	// The relayed native binary must be on the VM and run on musl (it is dynamically
	// linked against musl libs, which the Alpine sandbox has).
	bin := root + "/agent/claude/claude"
	if v := limaSh(t, remoteQuote(bin)+" --version 2>&1"); !strings.Contains(v, ".") {
		t.Errorf("claude --version = %q, want a version string", strings.TrimSpace(v))
	}

	// The auth env file exists, is 0600, blanks the API key, and is never world-readable.
	envFile := root + "/agent/claude/env.sh"
	perms := strings.Fields(limaSh(t, "ls -l "+envFile))
	if len(perms) == 0 || !strings.HasPrefix(perms[0], "-rw-------") {
		t.Errorf("env file perms = %v, want -rw------- (0600)", perms)
	}
	body := limaSh(t, "cat "+envFile)
	if !strings.Contains(body, "CLAUDE_CODE_OAUTH_TOKEN=") || !strings.Contains(body, "ANTHROPIC_API_KEY=\n") {
		t.Errorf("env file missing token export or blanked API key")
	}

	// The nana launcher + per-agent run script must be installed and executable.
	for _, p := range []string{root + "/nana", root + "/agent/claude/run"} {
		perms := strings.Fields(limaSh(t, "ls -l "+p))
		if len(perms) == 0 || perms[0][0] != '-' || !strings.Contains(perms[0], "x") {
			t.Errorf("%s missing or not executable: %v", p, perms)
		}
	}
	// `nana` with no agent arg resolves the sole installed agent and passes args
	// through to it — here `--version` runs the relayed binary, NANA.md wiring and all.
	if v := limaSh(t, remoteQuote(root+"/nana")+" --version 2>&1"); !strings.Contains(v, ".") {
		t.Errorf("nana --version = %q, want a version string", strings.TrimSpace(v))
	}

	// Run headless (no tty), nana mirrors the agent's stream to session.log so the
	// console can auto-tail it with no --agent-log. limactl shell (no -t) gives no
	// tty, so the capture branch fires.
	sessionLog := root + "/agent/claude/session.log"
	if out := limaSh(t, "cat "+sessionLog+" 2>/dev/null"); !strings.Contains(out, ".") {
		t.Errorf("session.log = %q, want the agent's version output captured for the [NANA] pane", strings.TrimSpace(out))
	}
}

// TestAgentLogBridge proves a serving Popo bridges the sandbox's per-agent
// session.log into the controller-side agent.log — the no-flag path that feeds
// `iceclimber logs`/`tui`/the console's [NANA] pane. We simulate a headless agent by
// writing a session.log in the sandbox, run `serve` in the background, and assert the
// line reaches the controller file.
func TestAgentLogBridge(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-bridge-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)
	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	// Stand in for a headless nana run: a plain line plus a claude stream-json
	// tool-call event in the session.log. The bridge should pass the plain line
	// through and render the stream-json into a readable "→ Bash: …" pane line.
	streamEvent := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"popo python.install 3.12"}}]}}`
	limaSh(t, "mkdir -p "+root+"/agent/claude && { printf 'nana: hello popo\\n'; printf '%s\\n' "+remoteQuote(streamEvent)+"; } > "+root+"/agent/claude/session.log")

	// Run serve under a private HOME so its controller-side agent.log lands in a temp
	// dir, not the operator's real ~/.iceclimber.
	_, agentLog := startServe(t, cfg)

	// The bridge polls every 1.5s; give it a generous window. Success = the plain
	// line AND the formatted tool-call line both reached the controller agent.log.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(agentLog)
		if strings.Contains(string(b), "nana: hello popo") && strings.Contains(string(b), "→ Bash: popo python.install 3.12") {
			return // bridged + stream-json formatted — success
		}
		time.Sleep(500 * time.Millisecond)
	}
	b, _ := os.ReadFile(agentLog)
	t.Fatalf("serve did not bridge the sandbox session.log to %s; have: %q", agentLog, b)
}

// TestServeResetsStaleAgentLog is the regression guard for the "stale [NANA]" bug: a
// new serving session must start the controller-side agent.log fresh, so it never
// shows a previous run's (or a leftover test's) agent stream as if it were live.
func TestServeResetsStaleAgentLog(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-reset-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)
	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	// Seed a stale agent.log under a private HOME, as a prior session would have left.
	home := t.TempDir()
	agentLog := filepath.Join(home, ".iceclimber", sandboxName, "agent.log")
	if err := os.MkdirAll(filepath.Dir(agentLog), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentLog, []byte("STALE: install Python sent to popo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	serve := exec.Command(iceclimberBin, "serve", "--yes", "--config", cfg, "--transport", "sftp")
	serve.Env = append(os.Environ(), "HOME="+home)
	if err := serve.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	defer func() { _ = serve.Process.Kill(); _, _ = serve.Process.Wait() }()

	// serve resets agent.log at startup; the stale line must disappear quickly.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(agentLog); !strings.Contains(string(b), "STALE") {
			return // reset cleared the prior session's stream — success
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("serve did not reset the stale agent.log — [NANA] would show a previous session's stream")
}

// TestAgentInstallRejectsAPIKey proves the command refuses an API-key token.
func TestAgentInstallRejectsAPIKey(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-agent-rej-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)

	tokenFile := filepath.Join(t.TempDir(), "apikey.env")
	if err := os.WriteFile(tokenFile, []byte("export CLAUDE_CODE_OAUTH_TOKEN=sk-ant-api03-deadbeef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(iceclimberBin, "agent", "install", "claude",
		"--token-file", tokenFile, "--transport", "sftp", "--config", cfg).CombinedOutput()
	if err == nil {
		t.Fatalf("agent install accepted an API key; want failure:\n%s", out)
	}
	if !strings.Contains(string(out), "API key") {
		t.Errorf("rejection message unexpected:\n%s", out)
	}
}
