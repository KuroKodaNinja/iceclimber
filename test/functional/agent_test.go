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
	return startServeTransport(t, cfg, "sftp")
}

// startServeTransport is startServe with an explicit remote FS transport (sftp|exec), so
// the agent-log bridge can be exercised over both channels.
func startServeTransport(t *testing.T, cfg, transport string) (*exec.Cmd, string) {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command(iceclimberBin, "serve", "--yes", "--config", cfg, "--transport", transport)
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

	// The CLI install recorded the agent identity in capabilities.json (#8): name, a
	// real (non-empty) version, and auth_configured (a token was provided). Guards the
	// recordAgentCaps call in the install command — independent of the console path.
	caps := limaSh(t, "cat "+root+"/protocol/capabilities.json")
	for _, want := range []string{`"name":"claude"`, `"display_name":"Claude Code"`, `"auth_configured":true`, `"version":"`} {
		if !strings.Contains(caps, want) {
			t.Errorf("capabilities.json missing %q:\n%s", want, caps)
		}
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
	for _, transport := range []string{"sftp", "exec"} {
		t.Run(transport, func(t *testing.T) {
			sb := requireSandbox(t)
			root := "/tmp/iceclimber-bridge-" + protocol.NewID()
			cfg := writeConfigRoot(t, sb, root)
			runIceclimber(t, "bootstrap", "--config", cfg, "--transport", transport)

			// The bridge seeds each session.log's offset to EOF on first sight (streams only
			// NEW output — no re-dump of a prior run), so create an EMPTY log before serve and
			// APPEND after. Stand in for a headless nana run: a plain line plus a claude
			// stream-json tool-call event; the bridge passes the plain line through and renders
			// the stream-json into a readable "→ Bash: …" pane line.
			streamEvent := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"popo python.install 3.12"}}]}}`
			limaSh(t, "mkdir -p "+root+"/agent/claude && : > "+root+"/agent/claude/session.log")

			// Run serve under a private HOME so its controller-side agent.log lands in a temp
			// dir, not the operator's real ~/.iceclimber.
			_, agentLog := startServeTransport(t, cfg, transport)

			// Append output on a loop (each ~1s) until it streams through: the very first
			// appends may land before the bridge's first poll (seeded past), but once seeded
			// every later append streams. This is robust to serve's connect latency.
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				limaSh(t, "{ printf 'nana: hello popo\\n'; printf '%s\\n' "+remoteQuote(streamEvent)+"; } >> "+root+"/agent/claude/session.log")
				b, _ := os.ReadFile(agentLog)
				if strings.Contains(string(b), "nana: hello popo") && strings.Contains(string(b), "→ Bash: popo python.install 3.12") {
					return // bridged incrementally + stream-json formatted — success
				}
				time.Sleep(1 * time.Second)
			}
			b, _ := os.ReadFile(agentLog)
			t.Fatalf("serve did not bridge the sandbox session.log to %s; have: %q", agentLog, b)
		})
	}
}

// TestServeDoesNotReimportPriorSession is the regression guard for the agent-log
// re-import bug: a fresh serve must NOT re-dump the sandbox session.log's pre-existing
// (prior-run) output into the controller agent.log — the bridge seeds each file's offset
// to EOF on first sight, so only output produced AFTER serve attaches streams. (The old
// truncate-reset was dropped: truncating the shared controller log is a multi-writer
// clobber hazard; EOF-seed achieves "only this run" without blowing away the file.)
func TestServeDoesNotReimportPriorSession(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-noreimport-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)
	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	// A prior session's output already sits in the sandbox session.log before serve starts.
	limaSh(t, "mkdir -p "+root+"/agent/claude && printf 'OLD-PRIOR-SESSION line\\n' > "+root+"/agent/claude/session.log")

	_, agentLog := startServe(t, cfg)

	// Drive NEW output on a loop until it streams — proving the bridge is live and seeded.
	deadline := time.Now().Add(30 * time.Second)
	streamed := false
	for time.Now().Before(deadline) {
		limaSh(t, "printf 'NEW-AFTER-ATTACH line\\n' >> "+root+"/agent/claude/session.log")
		if b, _ := os.ReadFile(agentLog); strings.Contains(string(b), "NEW-AFTER-ATTACH") {
			streamed = true
			break
		}
		time.Sleep(1 * time.Second)
	}
	if !streamed {
		t.Fatal("bridge never streamed new output — cannot assert no-reimport")
	}
	// The bridge is live and has streamed post-attach output; the pre-existing prior-session
	// line must never have been re-imported into the controller agent.log.
	if b, _ := os.ReadFile(agentLog); strings.Contains(string(b), "OLD-PRIOR-SESSION") {
		t.Fatalf("serve re-imported the prior session's output — agent.log has:\n%s", b)
	}
}

// TestAgentWrapPreexisting proves `agent wrap` wires the nana/run launcher around a
// binary ALREADY on the sandbox, with NO relay. We use /bin/echo as a stand-in
// pre-existing agent binary (it tolerates --version), wrap it, and confirm the
// launcher bakes that path, the nana dispatcher is written, nothing was relayed
// into the agent dir, and `nana` actually runs the wrapped binary.
func TestAgentWrapPreexisting(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-wrap-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)
	scheduleRootCleanup(t, root)
	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	out := string(runIceclimber(t, "agent", "wrap", "claude",
		"--bin", "/bin/echo", "--skip-auth", "--transport", "sftp", "--config", cfg))
	if !strings.Contains(out, "wrapped Claude Code") || !strings.Contains(out, "not relayed") {
		t.Errorf("wrap output unexpected:\n%s", out)
	}

	// The run launcher bakes the pre-existing binary; nana + run are executable.
	if s := limaSh(t, "cat "+root+"/agent/claude/run 2>/dev/null"); !strings.Contains(s, "bin='/bin/echo'") {
		t.Errorf("run launcher should bake /bin/echo:\n%s", s)
	}
	for _, p := range []string{root + "/nana", root + "/agent/claude/run"} {
		if s := limaSh(t, "test -x "+p+" && echo ok || echo no"); !strings.Contains(s, "ok") {
			t.Errorf("%s missing or not executable", p)
		}
	}
	// No relay: no agent binary was pushed into the agent dir.
	if s := limaSh(t, "test -e "+root+"/agent/claude/claude && echo present || echo absent"); !strings.Contains(s, "absent") {
		t.Errorf("wrap must not relay a binary into the agent dir")
	}
	// nana launches the wrapped binary and exits cleanly.
	if s := limaSh(t, root+"/nana --version >/dev/null 2>&1; echo EXIT=$?"); !strings.Contains(s, "EXIT=0") {
		t.Errorf("nana over the wrapped binary did not run cleanly: %s", s)
	}

	// The CLI wrap recorded the agent in capabilities.json (#8): name + auth not
	// configured (--skip-auth), no version (wrap resolves none). Guards the
	// recordAgentCaps call in the wrap command.
	caps := limaSh(t, "cat "+root+"/protocol/capabilities.json")
	if !strings.Contains(caps, `"name":"claude"`) || !strings.Contains(caps, `"auth_configured":false`) {
		t.Errorf("capabilities.json should record the wrapped agent (claude, auth not configured):\n%s", caps)
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
