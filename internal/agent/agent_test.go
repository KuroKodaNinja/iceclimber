package agent

import (
	"strings"
	"testing"
)

func TestLookupAndNames(t *testing.T) {
	if _, ok := Lookup("claude"); !ok {
		t.Fatal("claude not found")
	}
	if _, ok := Lookup("nope"); ok {
		t.Error("unknown agent reported found")
	}
	names := Names()
	if len(names) == 0 || names[0] != "claude" {
		t.Errorf("Names() = %v, want claude present", names)
	}
}

func TestLooksLikeAPIKey(t *testing.T) {
	// API keys (sk-ant-api…) must be rejected.
	if !LooksLikeAPIKey("sk-ant-api03-abc123") || !LooksLikeAPIKey("  sk-ant-api03-xyz  ") {
		t.Error("API keys not detected")
	}
	// Subscription OAuth tokens from `claude setup-token` (sk-ant-oat…) must be
	// ACCEPTED — they share the sk-ant- prefix but are not API keys.
	if LooksLikeAPIKey("sk-ant-oat01-abc123") {
		t.Error("subscription OAuth token wrongly flagged as an API key")
	}
	if LooksLikeAPIKey("a-real-oauth-token") {
		t.Error("OAuth token misidentified as API key")
	}
}

func TestPlatformPackage(t *testing.T) {
	cases := []struct {
		os, arch, libc, want string
		wantErr              bool
	}{
		{os: "linux", arch: "aarch64", libc: "musl", want: "@anthropic-ai/claude-code-linux-arm64-musl"},
		{os: "linux", arch: "x86_64", libc: "glibc", want: "@anthropic-ai/claude-code-linux-x64"},
		{os: "linux", arch: "aarch64", libc: "glibc", want: "@anthropic-ai/claude-code-linux-arm64"},
		{os: "darwin", arch: "aarch64", libc: "", want: "@anthropic-ai/claude-code-darwin-arm64"},
		{os: "plan9", arch: "aarch64", libc: "musl", wantErr: true},
		{os: "linux", arch: "riscv64", libc: "musl", wantErr: true},
	}
	for _, c := range cases {
		got, err := Claude.PlatformPackage(c.os, c.arch, c.libc)
		if c.wantErr {
			if err == nil {
				t.Errorf("PlatformPackage(%s,%s,%s) = %q, want error", c.os, c.arch, c.libc, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("PlatformPackage(%s,%s,%s) = %q,%v; want %q", c.os, c.arch, c.libc, got, err, c.want)
		}
	}
}

func TestRenderEnv(t *testing.T) {
	got := renderEnv(Claude, "tok-secret-123", "/opt/iceclimber/agent/claude")

	if !strings.Contains(got, "export CLAUDE_CODE_OAUTH_TOKEN='tok-secret-123'") {
		t.Errorf("token not exported/quoted:\n%s", got)
	}
	// API key blanked so it can never fall back to metered billing.
	if !strings.Contains(got, "export ANTHROPIC_API_KEY=\n") {
		t.Errorf("ANTHROPIC_API_KEY not blanked:\n%s", got)
	}
	if !strings.Contains(got, "export USE_BUILTIN_RIPGREP='0'") {
		t.Errorf("ripgrep workaround missing:\n%s", got)
	}
	if !strings.Contains(got, "export PATH='/opt/iceclimber/agent/claude':\"$PATH\"") {
		t.Errorf("agent dir not on PATH:\n%s", got)
	}
}

func TestRenderRun(t *testing.T) {
	got := renderRun(Claude, "/r/agent/claude", "/r/agent/claude/claude", "/r/skill/NANA.md")
	for _, want := range []string{
		"#!/bin/sh",
		`. "$self/env.sh"`,        // sources auth/env when present
		`nana='/r/skill/NANA.md'`, // references NANA.md
		`exec '/r/agent/claude/claude' '--append-system-prompt' "$sp" "$@"`, // wired + passthrough
	} {
		if !strings.Contains(got, want) {
			t.Errorf("run script missing %q:\n%s", want, got)
		}
	}
}

// An agent with no system-prompt flag still gets a launcher (no NANA wiring).
func TestRenderRun_NoSystemPromptFlag(t *testing.T) {
	d := Descriptor{Name: "x", DisplayName: "X", Bin: "x"}
	got := renderRun(d, "/r/agent/x", "/r/agent/x/x", "/r/skill/NANA.md")
	// No system-prompt flag → no NANA wiring in the executed command (the header
	// comment may still mention it; we assert the functional absence).
	if strings.Contains(got, "append-system-prompt") || strings.Contains(got, "nana=") {
		t.Errorf("agent without a system-prompt flag should not wire NANA into the exec:\n%s", got)
	}
	if !strings.Contains(got, `exec '/r/agent/x/x' "$@"`) {
		t.Errorf("missing plain exec:\n%s", got)
	}
}

func TestRenderDispatcher(t *testing.T) {
	got := renderDispatcher("/r")
	for _, want := range []string{
		`agents='/r/agent'`,
		`"$agents"/*/run`, // discovers installed agents
		`[ -f "$agents/$1/run" ]; then sel="$1"; shift`, // explicit selection
		`"$n" -eq 1`,                   // sole-default
		`multiple agents installed`,    // ambiguity error
		`[ "${1:-}" = "--" ] && shift`, // strips an explicit separator
		`exec "$agents/$sel/run" "$@"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dispatcher missing %q:\n%s", want, got)
		}
	}
}

// A token with shell metacharacters must be quoted so it can't break the env file.
func TestRenderEnv_QuotesNastyToken(t *testing.T) {
	got := renderEnv(Claude, "a'b;rm -rf /", "/bin")
	if strings.Contains(got, "rm -rf /\n") && !strings.Contains(got, `'a'\''b;rm -rf /'`) {
		t.Errorf("token not safely quoted:\n%s", got)
	}
}
