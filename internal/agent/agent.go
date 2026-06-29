// Package agent installs coding agents into the sandbox. An agent (Claude Code is
// the first) is distributed as a self-contained, per-platform native binary inside
// an npm package (e.g. @anthropic-ai/claude-code-linux-arm64-musl). Installing one
// is therefore a pure **relay**, honouring iceclimber's air-gap model: the
// controller downloads the package for the *sandbox's* platform on its own network
// and relays the binary in — the sandbox never needs the registry. New agents are
// added as another Descriptor — no new install machinery.
package agent

import (
	"fmt"
	"sort"
	"strings"
)

// EnvVar is one runtime environment entry the agent needs in the sandbox.
type EnvVar struct{ Key, Value string }

// Descriptor describes how to install and run one agent.
type Descriptor struct {
	Name        string   // CLI identifier, e.g. "claude"
	DisplayName string   // human label, e.g. "Claude Code"
	NpmPrefix   string   // platform package = NpmPrefix + "-" + platformSuffix(os,arch,libc)
	BinaryPath  string   // executable's path within the package (after the "package/" root)
	Bin         string   // installed executable name
	TokenEnv    string   // env var carrying the subscription token, e.g. CLAUDE_CODE_OAUTH_TOKEN
	APIKeyEnv   string   // API-key env var to BLANK so it can't fall back to metered billing
	Env         []EnvVar // extra runtime env written into the agent's env file
	VersionArgs []string // args that print the version (used to verify the install)
	// PrintFlags are the harness's non-interactive/headless flags (e.g. -p, --print).
	// nana captures the session to session.log when one is present (or stdout is not
	// a tty) — never for an interactive run, whose TUI needs the tty untouched.
	PrintFlags []string
	// SystemPromptFlag is the harness flag that appends a string to the agent's
	// system prompt; the `nana` launcher passes NANA.md's contents to it so the
	// Popo contract is persistent context every turn. Empty = the harness has no
	// such flag (nana then launches it without wiring NANA.md in).
	SystemPromptFlag string
}

// Claude is the Claude Code agent. Its native binary is dynamically linked against
// musl, so the sandbox must have musl + libstdc++/libgcc (an Alpine box does).
// USE_BUILTIN_RIPGREP=0 because the bundled ripgrep is glibc-built and won't run on
// musl — the sandbox provides `rg`.
var Claude = Descriptor{
	Name:        "claude",
	DisplayName: "Claude Code",
	NpmPrefix:   "@anthropic-ai/claude-code",
	BinaryPath:  "claude",
	Bin:         "claude",
	TokenEnv:    "CLAUDE_CODE_OAUTH_TOKEN",
	APIKeyEnv:   "ANTHROPIC_API_KEY",
	Env: []EnvVar{
		{Key: "USE_BUILTIN_RIPGREP", Value: "0"},
		{Key: "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", Value: "1"},
	},
	VersionArgs:      []string{"--version"},
	PrintFlags:       []string{"-p", "--print"},
	SystemPromptFlag: "--append-system-prompt",
}

// registry holds the known agents. Add a Descriptor here to support a new agent.
var registry = []Descriptor{Claude}

// Lookup returns the agent with the given name.
func Lookup(name string) (Descriptor, bool) {
	for _, d := range registry {
		if d.Name == name {
			return d, true
		}
	}
	return Descriptor{}, false
}

// All returns the known agents.
func All() []Descriptor { return append([]Descriptor(nil), registry...) }

// Names returns the known agent names, sorted.
func Names() []string {
	ns := make([]string, 0, len(registry))
	for _, d := range registry {
		ns = append(ns, d.Name)
	}
	sort.Strings(ns)
	return ns
}

// PlatformPackage maps a sandbox fingerprint to the per-platform npm package that
// carries the agent's native binary.
func (d Descriptor) PlatformPackage(os, arch, libc string) (string, error) {
	suffix, err := platformSuffix(os, arch, libc)
	if err != nil {
		return "", err
	}
	return d.NpmPrefix + "-" + suffix, nil
}

// platformSuffix renders the os/arch/libc fingerprint into npm's platform-package
// convention (e.g. "linux-arm64-musl", "darwin-x64").
func platformSuffix(os, arch, libc string) (string, error) {
	var o string
	switch os {
	case "linux":
		o = "linux"
	case "darwin":
		o = "darwin"
	default:
		return "", fmt.Errorf("unsupported agent OS %q", os)
	}
	var a string
	switch arch {
	case "x86_64", "amd64", "x64":
		a = "x64"
	case "aarch64", "arm64":
		a = "arm64"
	default:
		return "", fmt.Errorf("unsupported agent arch %q", arch)
	}
	s := o + "-" + a
	if o == "linux" && libc == "musl" {
		s += "-musl"
	}
	return s, nil
}

// LooksLikeAPIKey reports whether s is an Anthropic API key, which must be rejected:
// agents run on a subscription OAuth token only (never metered API billing). API
// keys are "sk-ant-api…"; subscription OAuth tokens from `claude setup-token` are
// "sk-ant-oat…" — both share the "sk-ant-" prefix, so only the "-api" form is an
// API key.
func LooksLikeAPIKey(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), "sk-ant-api")
}
