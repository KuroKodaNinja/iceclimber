// Package config loads and validates the operator-owned iceclimber.yaml. This
// file is never written by Nana. It models the full operator surface: SSH/transport,
// per-language runtime source + pins, package-manager mirrors + Tier-1 controllers,
// egress mode + web.fetch venue routing/rewrites/approvals, and the log-rotation knobs.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/egress"
	"gopkg.in/yaml.v3"
)

// Config is the controller's view of one sandbox.
type Config struct {
	SandboxID  string `yaml:"sandbox_id"`
	SSH        SSH    `yaml:"ssh"`
	RemoteRoot string `yaml:"remote_root"` // optional; chosen during bootstrap if empty
	CacheDir   string `yaml:"cache_dir"`
	// Catalog lists extra tool-catalog files/dirs (YAML) layered over the embedded
	// defaults + the ~/.iceclimber/catalog convention; later entries override by name.
	Catalog []string `yaml:"catalog"`
	Pip     Pip      `yaml:"pip"`
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
	// ControllerCargo is the operator's cargo on the controller, used by cargo.build's relay
	// tier to vendor every crate the sandbox builds from offline (air-gapped `cargo build
	// --offline`). Defaults to "cargo" at use; the relay tier errors clearly if absent.
	ControllerCargo string `yaml:"controller_cargo"`
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
	// RuntimePins optionally pins exact managed-runtime assets (url + sha256 per
	// platform), keyed lang → requested-version → pin. A pin bypasses the live upstream
	// resolver for that language/version/platform (the air-gapped-mirror path, §17); an
	// uncovered box falls through to the normal resolve. Consulted by the python/node/java
	// installers before any network metadata call.
	RuntimePins map[string]map[string]RuntimePin `yaml:"runtime_pins"`
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
	// DispatchConcurrency is how many requests Popo services at once (the bounded worker pool,
	// so an agent's parallel requests / subagents don't queue behind a slow install). Empty/0 =
	// the default (see Concurrency); clamped to >= 1. See PROTOCOL.md "Concurrency".
	DispatchConcurrency int `yaml:"dispatch_concurrency"`
	// LogMaxBytes / LogKeep bound the controller log sinks (activity, egress audit, the bridged
	// agent stream): each rotates at LogMaxBytes and keeps LogKeep generations. 0 = the built-in
	// defaults (see internal/logfile: 8 MiB × 4). Applies to the sandbox session.log too.
	LogMaxBytes int64 `yaml:"log_max_bytes"`
	LogKeep     int   `yaml:"log_keep"`
}

// DefaultDispatchConcurrency is the worker-pool size applied when dispatch_concurrency is unset.
// Conservative: comfortably under a typical sshd MaxSessions (~10) even with the heartbeat +
// per-op exec sessions, and safe for relay installs that also spend controller CPU/network.
const DefaultDispatchConcurrency = 4

// Concurrency returns the dispatch worker-pool size: the default when unset/0, else the
// configured value clamped to at least 1 (0 would stall serving; negatives are nonsense).
func (c *Config) Concurrency() int {
	if c.DispatchConcurrency == 0 {
		return DefaultDispatchConcurrency
	}
	if c.DispatchConcurrency < 1 {
		return 1
	}
	return c.DispatchConcurrency
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

// RuntimePin is one operator-pinned runtime version: an optional exact full version (for
// the install dir; defaults to the requested key) and a per-platform asset table keyed by
// "os/arch/libc" (the same fingerprint keys the tool catalog uses).
type RuntimePin struct {
	Full      string                     `yaml:"full"`
	Platforms map[string]RuntimePinAsset `yaml:"platforms"`
}

// RuntimePinAsset is a pinned download: a URL and its sha256 (mandatory — an unverifiable
// pin is rejected at load, the same air-gap floor as the tool catalog).
type RuntimePinAsset struct {
	URL    string `yaml:"url"`
	SHA256 string `yaml:"sha256"`
}

// LookupRuntimePin resolves the pin for (lang, requested version) on a platform from a
// runtime_pins table: the asset URL + sha256 and the full version for the install dir.
// ok=false means no pin covers this box — the caller falls through to the live resolver.
// full defaults to the requested version. Standalone (not a *Config method) so callers
// holding just the table — the CLI session — can resolve without a Config value.
func LookupRuntimePin(pins map[string]map[string]RuntimePin, lang, version, os, arch, libc string) (url, sha256, full string, ok bool) {
	byVer, ok := pins[lang]
	if !ok {
		return "", "", "", false
	}
	pin, ok := byVer[version]
	if !ok {
		return "", "", "", false
	}
	a, ok := pin.Platforms[os+"/"+arch+"/"+libc]
	if !ok {
		return "", "", "", false
	}
	full = pin.Full
	if full == "" {
		full = version
	}
	return a.URL, a.SHA256, full, true
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

// DefaultPath is the conventional config filename the CLI defaults --config to. Only
// this default engages the --sandbox search path below; an explicit --config never does.
const DefaultPath = "iceclimber.yaml"

// Load reads, parses, and validates the config at path. When selectSandbox is non-empty
// it must match the configured sandbox_id — a guard against acting on the wrong sandbox.
//
// --sandbox is also a SELECTOR: when path is the default and doesn't yield sandbox X,
// Load searches the per-sandbox conventions `iceclimber-<X>.yaml` (cwd) and
// `~/.iceclimber/<X>/config.yaml`, so one directory (or the home state dir) can hold a
// config per sandbox and every command addresses any of them by id. Reserved for the
// fleet phase (rejected with a clear error today): "all", comma lists, and glob patterns.
func Load(path, selectSandbox string) (*Config, error) {
	if isFleetSelector(selectSandbox) {
		return nil, fmt.Errorf("--sandbox %q: fleet selection (all / lists / globs) arrives with multi-sandbox support — name a single sandbox for now", selectSandbox)
	}
	c, err := loadOne(path, selectSandbox)
	if err == nil {
		return c, nil
	}
	if path != DefaultPath {
		return nil, err // explicit --config: exact semantics, no search
	}
	if selectSandbox != "" {
		for _, cand := range sandboxSearchPaths(selectSandbox) {
			if c2, err2 := loadOne(cand, selectSandbox); err2 == nil {
				return c2, nil
			}
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no config for sandbox %q — looked at %s, %s; run `iceclimber init` to scaffold one, or pass --config",
				selectSandbox, DefaultPath, strings.Join(sandboxSearchPaths(selectSandbox), ", "))
		}
		return nil, fmt.Errorf("%w (also looked for %s)", err, strings.Join(sandboxSearchPaths(selectSandbox), ", "))
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no config at %s — run `iceclimber init` to scaffold one, or pass --config", path)
	}
	return nil, err
}

// loadOne reads, parses, and validates a single config file.
func loadOne(path, selectSandbox string) (*Config, error) {
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

// sandboxSearchPaths are the per-sandbox config conventions --sandbox selects from when
// the default path doesn't yield the requested sandbox.
func sandboxSearchPaths(id string) []string {
	paths := []string{"iceclimber-" + id + ".yaml"}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".iceclimber", id, "config.yaml"))
	}
	return paths
}

// isFleetSelector reports whether a --sandbox value names more than one sandbox — the
// namespace is reserved now so the fleet phase can give it meaning without a breaking change.
func isFleetSelector(s string) bool {
	return s == "all" || strings.ContainsAny(s, "*?[,")
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
		case "", "managed", "system":
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
	// Surface a bad maildir_retention here rather than silently falling back to the default at
	// use (Retention()) — a typo like "1hr" would otherwise disable nothing yet mislead.
	if c.MaildirRetention != "" {
		if _, err := time.ParseDuration(c.MaildirRetention); err != nil {
			return fmt.Errorf("maildir_retention %q is not a valid duration (e.g. \"1h\", \"30m\", \"0\"): %w", c.MaildirRetention, err)
		}
	}
	if c.DispatchConcurrency < 0 {
		return fmt.Errorf("dispatch_concurrency %d must be >= 0 (0 = default %d)", c.DispatchConcurrency, DefaultDispatchConcurrency)
	}
	if c.LogMaxBytes < 0 {
		return fmt.Errorf("log_max_bytes %d must be >= 0 (0 = default)", c.LogMaxBytes)
	}
	if c.LogKeep < 0 {
		return fmt.Errorf("log_keep %d must be >= 0 (0 = default)", c.LogKeep)
	}
	for i, d := range c.Network.AllowedDomains {
		if err := egress.ValidateHostPattern(d.Pattern); err != nil {
			return fmt.Errorf("network.allowed_domains[%d]: %w", i, err)
		}
	}
	// Runtime pins: every pinned asset must carry a url + sha256 (an unverifiable pin is
	// rejected — the air-gap supply-chain floor), and platform keys must be os/arch/libc.
	for lang, byVer := range c.RuntimePins {
		for ver, pin := range byVer {
			for key, a := range pin.Platforms {
				where := fmt.Sprintf("runtime_pins.%s.%q.%s", lang, ver, key)
				if len(strings.Split(key, "/")) != 3 {
					return fmt.Errorf("%s: platform key must be os/arch/libc (e.g. linux/aarch64/musl)", where)
				}
				if a.URL == "" || a.SHA256 == "" {
					return fmt.Errorf("%s: needs both url and sha256 (an unverifiable pin is rejected)", where)
				}
			}
		}
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
