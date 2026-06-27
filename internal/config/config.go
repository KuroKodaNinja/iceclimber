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

// SSH holds the controller's connection details for the sandbox host.
type SSH struct {
	Host         string `yaml:"host"`
	User         string `yaml:"user"`
	Port         int    `yaml:"port"`
	IdentityFile string `yaml:"identity_file"` // optional; falls back to ssh-agent
	KnownHosts   string `yaml:"known_hosts"`   // optional; defaults to ~/.ssh/known_hosts
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
	if c.SSH.Port == 0 {
		c.SSH.Port = 22
	}
	c.SSH.IdentityFile = expandHome(c.SSH.IdentityFile)
	c.SSH.KnownHosts = expandHome(c.SSH.KnownHosts)
	c.CacheDir = expandHome(c.CacheDir)
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
	if c.SSH.User == "" {
		missing = append(missing, "ssh.user")
	}
	if len(missing) > 0 {
		return fmt.Errorf("config missing required field(s): %s", strings.Join(missing, ", "))
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
