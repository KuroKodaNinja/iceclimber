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
	"time"

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
	// ControllerConda is the operator's conda on the controller, used for air-gapped
	// (relay-tier) conda: it resolves + downloads packages the sandbox installs offline.
	// Defaults to "conda" at use; the relay tier skips/errors clearly if absent.
	ControllerConda string `yaml:"controller_conda"`
	// ControllerMvn is the operator's Maven on the controller, used by maven.build to
	// prime an offline .m2 repo the sandbox builds from (air-gapped `mvn -o package`).
	// Defaults to "mvn" at use; maven.build errors clearly if absent.
	ControllerMvn string `yaml:"controller_mvn"`
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
	// Runtimes optionally pins where each language runtime comes from (managed vs a
	// pre-existing system runtime). An explicit override of the bootstrap choice;
	// unset languages default to managed. (§ pre-existing runtimes)
	Runtimes RuntimesConfig `yaml:"runtimes"`
	// MaildirRetention is how long a delivered-but-uncollected response may sit in
	// inbox/new before GC reaps it (with its request). A Go duration string ("1h",
	// "30m"); empty = the 1h default; "0" disables the retention sweep (collected pairs
	// are still pruned). See Config.Retention.
	MaildirRetention string `yaml:"maildir_retention"`
	// EgressMode selects how the sandbox obtains packages: "proxy" (default — the sandbox's
	// own package managers reach real registries through a controller-run MITM proxy over
	// the SSH reverse tunnel; still no direct sandbox internet) or "relay" (the controller
	// resolves + relays artifacts in; the sandbox never opens a connection at all — the
	// stricter air-gap for compliance regimes that want only relayed files). Empty = proxy.
	EgressMode string `yaml:"egress_mode"`
	// EgressProxyPort is the sandbox loopback port the reverse tunnel exposes the proxy on
	// (what the sandbox's HTTPS_PROXY points at). Empty/0 = the default (see EgressPort).
	EgressProxyPort int `yaml:"egress_proxy_port"`
}

// DefaultMaildirRetention is the retention window applied when maildir_retention is unset.
const DefaultMaildirRetention = time.Hour

// Retention returns the parsed maildir retention window: the 1h default when unset (or
// unparseable — a bad value never silently disables GC), else the configured duration
// ("0" disables the retention sweep).
func (c *Config) Retention() time.Duration {
	if c.MaildirRetention == "" {
		return DefaultMaildirRetention
	}
	d, err := time.ParseDuration(c.MaildirRetention)
	if err != nil {
		return DefaultMaildirRetention
	}
	return d
}

// DefaultEgressProxyPort is the sandbox loopback port the egress proxy is tunneled to
// when egress_proxy_port is unset.
const DefaultEgressProxyPort = 18080

// EgressProxy reports whether egress runs through the MITM proxy — the default; only an
// explicit egress_mode: relay opts out (to the stricter relay-only air-gap).
func (c *Config) EgressProxy() bool { return !strings.EqualFold(c.EgressMode, "relay") }

// EgressRelay reports whether egress uses the relay tier (explicit egress_mode: relay).
func (c *Config) EgressRelay() bool { return strings.EqualFold(c.EgressMode, "relay") }

// EgressPort is the configured sandbox loopback proxy port, or the default.
func (c *Config) EgressPort() int {
	if c.EgressProxyPort > 0 {
		return c.EgressProxyPort
	}
	return DefaultEgressProxyPort
}

// RuntimesConfig is the operator's per-language runtime-source override.
type RuntimesConfig struct {
	Python RuntimePref `yaml:"python"`
	Node   RuntimePref `yaml:"node"`
	Java   RuntimePref `yaml:"java"`
}

// RuntimePref pins one language's runtime source. Source is "managed" | "system"
// (empty = unset). Path optionally pins a system interpreter; EnvManager picks the
// isolation tool for system mode (python: "venv" | "conda").
type RuntimePref struct {
	Source     string `yaml:"source"`
	Path       string `yaml:"path"`
	EnvManager string `yaml:"env_manager"`
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
	// KeepAliveInterval is the SSH keepalive ping interval in seconds. 0 uses the
	// 20s default; a negative value disables keepalives. Keeps the connection warm
	// through idle windows (long controller-side downloads, slow fetches) so a
	// corporate firewall/NAT/bastion doesn't silently drop it.
	KeepAliveInterval int `yaml:"keepalive_interval"`
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
	for lang, pref := range map[string]RuntimePref{"python": c.Runtimes.Python, "node": c.Runtimes.Node, "java": c.Runtimes.Java} {
		switch pref.Source {
		case "", "managed":
		case "system":
			if lang != "python" {
				return fmt.Errorf("runtimes.%s.source: system is not supported yet (only python); use managed", lang)
			}
		default:
			return fmt.Errorf("runtimes.%s.source %q must be \"managed\" or \"system\"", lang, pref.Source)
		}
		switch pref.EnvManager {
		case "", "venv", "conda":
			if pref.EnvManager != "" && lang != "python" {
				return fmt.Errorf("runtimes.%s.env_manager: only python has an env_manager", lang)
			}
		default:
			return fmt.Errorf("runtimes.%s.env_manager %q must be \"venv\" or \"conda\"", lang, pref.EnvManager)
		}
	}
	switch strings.ToLower(c.EgressMode) {
	case "", "relay", "proxy":
	default:
		return fmt.Errorf("egress_mode %q must be \"relay\" or \"proxy\"", c.EgressMode)
	}
	if c.EgressProxyPort < 0 || c.EgressProxyPort > 65535 {
		return fmt.Errorf("egress_proxy_port %d out of range (0 = default)", c.EgressProxyPort)
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
