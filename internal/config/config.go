// Package config loads and validates the operator-owned iceclimber.yaml. This
// file is never written by Nana. Only the fields needed by the current build
// phase are modeled; network/fetch_rewrites/approvals (§3, §6.1) land when
// web.fetch is implemented.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the controller's view of one sandbox.
type Config struct {
	SandboxID  string `yaml:"sandbox_id"`
	SSH        SSH    `yaml:"ssh"`
	RemoteRoot string `yaml:"remote_root"` // optional; chosen during bootstrap if empty
	CacheDir   string `yaml:"cache_dir"`
	Pip        Pip    `yaml:"pip"`
	// ControllerPython is the operator's python on the controller, used for
	// Tier-1 cross-platform wheel downloads. Defaults to "python3" at use.
	ControllerPython string `yaml:"controller_python"`
	// Npm configures npm package installs (§5).
	Npm Npm `yaml:"npm"`
	// ControllerNpm is the operator's npm on the controller, used for Tier-1
	// relay. Defaults to "npm" at use.
	ControllerNpm string `yaml:"controller_npm"`
	// Maven configures JVM (Maven-coordinate) dependency resolution (§5).
	Maven Maven `yaml:"maven"`
	// ControllerJava is the operator's java on the controller, used for Tier-1
	// JVM dependency relay. Defaults to "java" at use.
	ControllerJava string `yaml:"controller_java"`
	// AuditLog is the controller-side web.fetch audit JSONL path. Empty means
	// the default ~/.iceclimber/audit/<sandbox_id>.jsonl.
	AuditLog string `yaml:"audit_log"`
	// ActivityLog is the controller-side request/operator activity JSONL path
	// (what `serve` records and `iceclimber logs` tails). Empty means the default
	// ~/.iceclimber/<sandbox_id>/activity.jsonl.
	ActivityLog string `yaml:"activity_log"`
	// Network governs web.fetch venue routing + egress gating (§6.1).
	Network Network `yaml:"network"`
	// Rewrites redirect/re-venue fetch URLs before gating (§6.1).
	Rewrites []Rewrite `yaml:"fetch_rewrites"`
	// ApprovalsFile is the operator-owned allow/deny rule file (never Nana-writable).
	// Empty means ~/.iceclimber/<sandbox_id>/approvals.json; pending lives alongside.
	ApprovalsFile string `yaml:"approvals_file"`
}

// Network routes web.fetch venues and governs unlisted URLs (§3, §6.1).
type Network struct {
	AllowedDomains       []AllowedDomain `yaml:"allowed_domains"`
	UnlistedDomainPolicy string          `yaml:"unlisted_domain_policy"` // "gate" (default) | "deny"
}

// AllowedDomain maps a host pattern to the venue that can reach it.
type AllowedDomain struct {
	Pattern       string `yaml:"pattern"`
	ReachableFrom string `yaml:"reachable_from"` // "sandbox" | "controller"
}

// Rewrite redirects a matching URL and adopts a venue (§6.1).
type Rewrite struct {
	Match     string `yaml:"match"`
	RewriteTo string `yaml:"rewrite_to"`
	Venue     string `yaml:"venue"` // "sandbox" | "controller"
}

// Npm configures npm package install (§5). RegistryURL is the Tier-0 mirror
// (sandbox-reachable); ControllerRegistry is the Tier-1 source Popo downloads
// from (defaults to the npm public registry at use).
type Npm struct {
	RegistryURL        string `yaml:"registry_url"`
	ControllerRegistry string `yaml:"controller_registry"`
}

// Maven configures JVM dependency resolution (§5). RepositoryURL is an optional
// sandbox-reachable Maven repository for Tier 0; ControllerRepository is the Tier-1
// source Popo resolves from (both empty = Maven Central).
type Maven struct {
	RepositoryURL        string `yaml:"repository_url"`
	ControllerRepository string `yaml:"controller_repository"`
}

// Pip configures package install (§5). IndexURL is the Tier-0 mirror
// (sandbox-reachable); ControllerIndexURL is the Tier-1 source Popo downloads
// from (defaults to PyPI at use).
type Pip struct {
	IndexURL           string `yaml:"index_url"`
	ExtraIndexURL      string `yaml:"extra_index_url"`
	TrustedHost        string `yaml:"trusted_host"`
	ControllerIndexURL string `yaml:"controller_index_url"`
}

// SSH holds the controller's connection details for the sandbox host. host may be
// a literal host/IP or a ~/.ssh/config Host alias; when use_ssh_config is on (the
// default) `ssh -G` resolves the alias (HostName/User/Port/IdentityFile/ProxyJump),
// abstracting jumpboxes away into the operator's existing ssh config.
type SSH struct {
	Host         string `yaml:"host"`
	User         string `yaml:"user"`          // optional; ssh_config / OS default supplies it
	Port         int    `yaml:"port"`          // optional; 0 = let ssh_config / default (22) decide
	IdentityFile string `yaml:"identity_file"` // optional; falls back to ssh-agent
	KnownHosts   string `yaml:"known_hosts"`   // optional; defaults to ~/.ssh/known_hosts

	// PasswordAuth / KeyboardInteractive opt into interactive auth (off by default;
	// key/agent are always tried first). Prompted no-echo on the controlling
	// terminal — works headless too, as long as a terminal exists.
	PasswordAuth        bool `yaml:"password_auth"`
	KeyboardInteractive bool `yaml:"keyboard_interactive"`
	// UseSSHConfig gates consulting ~/.ssh/config via `ssh -G`. Pointer so an unset
	// field means "default true"; set false to force a literal direct dial.
	UseSSHConfig *bool `yaml:"use_ssh_config"`
	// SSHConfigFile overrides the ssh config path used for resolution (ssh -F).
	SSHConfigFile string `yaml:"ssh_config_file"`
}

// Load reads, parses, and validates the config at path. When selectSandbox is
// non-empty it must match the configured sandbox_id — a guard against acting on
// the wrong sandbox. Fleet selection across multiple configs is future work (§8).
func Load(path, selectSandbox string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	// Port is intentionally NOT defaulted to 22 here: 0 means "unset" so an
	// ssh_config Port can win during resolution; the dial layer applies the 22
	// default last (after `ssh -G`). Display sites use portOr22 for a friendly value.
	c.SSH.IdentityFile = expandHome(c.SSH.IdentityFile)
	c.SSH.KnownHosts = expandHome(c.SSH.KnownHosts)
	c.SSH.SSHConfigFile = expandHome(c.SSH.SSHConfigFile)
	c.CacheDir = expandHome(c.CacheDir)
	c.AuditLog = expandHome(c.AuditLog)
	c.ActivityLog = expandHome(c.ActivityLog)
	if err := c.validate(selectSandbox); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) validate(selectSandbox string) error {
	var missing []string
	if c.SandboxID == "" {
		missing = append(missing, "sandbox_id")
	}
	if c.SSH.Host == "" {
		missing = append(missing, "ssh.host")
	}
	// ssh.user is optional: ssh_config (or the OS default) can supply it.
	if len(missing) > 0 {
		return fmt.Errorf("config missing required field(s): %s", strings.Join(missing, ", "))
	}
	// A host starting with '-' would be parsed by `ssh -G` as an option flag
	// (option injection); reject it. Real hosts/aliases never start with '-'.
	if strings.HasPrefix(c.SSH.Host, "-") {
		return fmt.Errorf("ssh.host %q must not start with '-'", c.SSH.Host)
	}
	if selectSandbox != "" && selectSandbox != c.SandboxID {
		return fmt.Errorf("--sandbox %q does not match configured sandbox_id %q", selectSandbox, c.SandboxID)
	}
	return nil
}

// expandHome expands a leading ~ or ~/ against the controller's home directory.
// The ~user form is not supported.
func expandHome(p string) string {
	if p == "" || !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	default:
		return p
	}
}
