//go:build functional

package functional

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

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
