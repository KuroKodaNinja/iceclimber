// Package pins is the single source of truth for every upstream version, source URL, and
// checksum iceclimber pins. The data lives in an embedded pins.yaml — the one place to edit to
// bump a pin. This package parses and VALIDATES it once at init and exposes typed accessors, so
// no version/URL/checksum constant is scattered across the runtime packages (they read here).
package pins

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed pins.yaml
var pinsYAML []byte

// Runtime holds one language runtime's pinned line and upstream sources (only the fields that
// apply to a given language are populated in pins.yaml).
type Runtime struct {
	RecommendedLine string `yaml:"recommended_line"`
	ManifestURL     string `yaml:"manifest_url"`    // python (python-build-standalone)
	DistBaseGlibc   string `yaml:"dist_base_glibc"` // node
	DistBaseMusl    string `yaml:"dist_base_musl"`  // node
	AdoptiumAPI     string `yaml:"adoptium_api"`    // java
	DistBase        string `yaml:"dist_base"`       // rust
}

// CoursierLauncher is the pinned (immutable-commit) Coursier launcher + its sha256.
type CoursierLauncher struct {
	URL    string `yaml:"url"`
	SHA256 string `yaml:"sha256"`
}

// GvisorRunsc is the pinned gVisor release + its per-arch (uname -m keyed) sha512s.
type GvisorRunsc struct {
	Release string            `yaml:"release"`
	SHA512  map[string]string `yaml:"sha512"`
}

// Tools holds the pinned non-runtime tools iceclimber fetches.
type Tools struct {
	MavenToolVersion string           `yaml:"maven_tool_version"`
	CoursierLauncher CoursierLauncher `yaml:"coursier_launcher"`
	GvisorRunsc      GvisorRunsc      `yaml:"gvisor_runsc"`
}

// Registry is the whole parsed pins.yaml.
type Registry struct {
	Runtimes map[string]Runtime `yaml:"runtimes"`
	Tools    Tools              `yaml:"tools"`
}

// P is the parsed, validated pin registry, loaded once at package init.
var P = mustLoad()

// Rt returns a runtime's pins by name, panicking if the name is absent — callers pass a
// compile-time-known language name, so an absent key is a pins.yaml editing bug, caught loudly.
func Rt(name string) Runtime {
	rt, ok := P.Runtimes[name]
	if !ok {
		panic("pins: no runtime pin named " + name)
	}
	return rt
}

func mustLoad() Registry {
	var r Registry
	if err := yaml.Unmarshal(pinsYAML, &r); err != nil {
		panic(fmt.Sprintf("pins: malformed embedded pins.yaml: %v", err))
	}
	// Validate every pin is present: a missing field would silently be "" and break a fetch far
	// from here. Fail loudly at init instead (any test exercising this package trips it).
	for _, name := range []string{"python", "node", "java", "rust"} {
		if rt, ok := r.Runtimes[name]; !ok || rt.RecommendedLine == "" {
			panic("pins: missing recommended_line for runtime " + name)
		}
	}
	if r.Runtimes["python"].ManifestURL == "" ||
		r.Runtimes["node"].DistBaseGlibc == "" || r.Runtimes["node"].DistBaseMusl == "" ||
		r.Runtimes["java"].AdoptiumAPI == "" || r.Runtimes["rust"].DistBase == "" {
		panic("pins: missing a runtime upstream source URL")
	}
	if r.Tools.MavenToolVersion == "" ||
		r.Tools.CoursierLauncher.URL == "" || r.Tools.CoursierLauncher.SHA256 == "" ||
		r.Tools.GvisorRunsc.Release == "" || len(r.Tools.GvisorRunsc.SHA512) == 0 {
		panic("pins: missing a tool pin (maven / coursier / gvisor)")
	}
	return r
}
