package config

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Fleet is a parsed multi-sandbox config (§14.1): a `defaults:` template deep-merged into
// each entry of a `sandboxes:` map. A classic single-sandbox file loads as a ONE-entry fleet,
// so every fleet-aware command has a uniform view. Members are validated post-merge.
type Fleet struct {
	Path    string
	members []*Config
	byID    map[string]*Config
	ids     []string
}

// fleetProbe detects the file shape and holds the raw member/defaults nodes for merging.
type fleetProbe struct {
	SandboxID string               `yaml:"sandbox_id"`
	Sandboxes map[string]yaml.Node `yaml:"sandboxes"`
	Defaults  yaml.Node            `yaml:"defaults"`
}

// IsFleetFile reports whether the file at path is a fleet config (a `sandboxes:` map).
func IsFleetFile(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && isFleetData(data)
}

// isFleetData reports whether raw YAML is a fleet file (has a non-empty `sandboxes:` map).
func isFleetData(data []byte) bool {
	var p fleetProbe
	if yaml.Unmarshal(data, &p) != nil {
		return false
	}
	return len(p.Sandboxes) > 0
}

// LoadFleet parses path as a fleet (a classic single-sandbox file becomes a one-entry fleet),
// merging + validating every member. Fleet-aware commands use this; single-box commands use Load.
func LoadFleet(path string) (*Fleet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if isFleetData(data) {
		return parseFleetData(path, data)
	}
	// classic single-sandbox file → a one-entry fleet
	c, err := loadOne(path, "")
	if err != nil {
		return nil, err
	}
	f := &Fleet{Path: path, byID: map[string]*Config{}}
	f.add(c)
	return f, nil
}

// parseFleetData merges each member onto `defaults` and validates it. The map KEY is the
// sandbox id (authoritative — it overrides any sandbox_id in defaults or the entry).
func parseFleetData(cfgPath string, data []byte) (*Fleet, error) {
	var p fleetProbe
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", cfgPath, err)
	}
	if p.SandboxID != "" {
		return nil, fmt.Errorf("config %s has both `sandbox_id` (single) and `sandboxes` (fleet) — use one shape", cfgPath)
	}
	if len(p.Sandboxes) == 0 {
		return nil, fmt.Errorf("config %s: `sandboxes` is empty", cfgPath)
	}
	f := &Fleet{Path: cfgPath, byID: map[string]*Config{}}
	for id, node := range p.Sandboxes {
		var c Config
		// defaults FIRST, then the member node onto the SAME struct: yaml.v3 assigns only keys
		// PRESENT in each document, so presence-vs-zero is exact (an entry's `password_auth:
		// false` overrides a `true` default; an absent key inherits). No pointer fields, no
		// custom merge code.
		if !p.Defaults.IsZero() {
			if err := p.Defaults.Decode(&c); err != nil {
				return nil, fmt.Errorf("config %s: defaults: %w", cfgPath, err)
			}
		}
		node := node
		if err := node.Decode(&c); err != nil {
			return nil, fmt.Errorf("config %s: sandbox %q: %w", cfgPath, id, err)
		}
		c.SandboxID = id
		finalize(&c)
		if err := c.validate(""); err != nil {
			return nil, fmt.Errorf("config %s: sandbox %q: %w", cfgPath, id, err)
		}
		f.add(&c)
	}
	return f, nil
}

// NewFleet builds a Fleet from already-loaded members (e.g. the per-sandbox registry, which
// `sandbox create`/`list` populate — the de-facto fleet). Duplicate ids are an error. label
// is a human tag for error messages (e.g. "registry").
func NewFleet(label string, members []*Config) (*Fleet, error) {
	f := &Fleet{Path: label, byID: map[string]*Config{}}
	for _, c := range members {
		if _, dup := f.byID[c.SandboxID]; dup {
			return nil, fmt.Errorf("duplicate sandbox id %q in %s", c.SandboxID, label)
		}
		f.add(c)
	}
	return f, nil
}

func (f *Fleet) add(c *Config) {
	f.byID[c.SandboxID] = c
	f.members = append(f.members, c)
	f.ids = append(f.ids, c.SandboxID)
	sort.Slice(f.members, func(i, j int) bool { return f.members[i].SandboxID < f.members[j].SandboxID })
	sort.Strings(f.ids)
}

// Members returns every member, sorted by id.
func (f *Fleet) Members() []*Config { return f.members }

// IDs returns the member ids, sorted.
func (f *Fleet) IDs() []string { return f.ids }

// Member returns the member with id, or (nil, false).
func (f *Fleet) Member(id string) (*Config, bool) { c, ok := f.byID[id]; return c, ok }

// single returns exactly one member for a single-box load: the one named by selector, or the
// sole member when selector is empty. Errors when a fleet has >1 member and no selector, or the
// selector names an absent member. selector must not be a fleet selector (Load guards that).
func (f *Fleet) single(selector string) (*Config, error) {
	if selector == "" {
		if len(f.members) == 1 {
			return f.members[0], nil
		}
		return nil, fmt.Errorf("config %s is a fleet of %d sandboxes (%s) — name one with --sandbox",
			f.Path, len(f.members), strings.Join(f.ids, ", "))
	}
	if c, ok := f.byID[selector]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("--sandbox %q: no such sandbox in %s (have: %s)", selector, f.Path, strings.Join(f.ids, ", "))
}

// Resolve turns a --sandbox selector (all / glob / comma-list / single id) into members,
// sorted and deduped. An empty selector is the caller's default to interpret (§14.2), so it is
// rejected here — callers apply their per-command default first.
func (f *Fleet) Resolve(selector string) ([]*Config, error) {
	if selector == "all" {
		return f.members, nil
	}
	if selector == "" {
		return nil, fmt.Errorf("no --sandbox selector given")
	}
	seen := map[string]bool{}
	var out []*Config
	for _, part := range strings.Split(selector, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		matched := false
		for _, c := range f.members {
			if ok, _ := path.Match(part, c.SandboxID); ok {
				if !seen[c.SandboxID] {
					seen[c.SandboxID] = true
					out = append(out, c)
				}
				matched = true
			}
		}
		if !matched {
			return nil, fmt.Errorf("--sandbox %q matched no sandbox in %s (have: %s)", part, f.Path, strings.Join(f.ids, ", "))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SandboxID < out[j].SandboxID })
	return out, nil
}
