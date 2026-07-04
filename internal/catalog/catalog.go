// Package catalog is the data-driven definition of installable utility tools (plan §16):
// a tool is a YAML entry — kind, per-{os,arch,libc} assets, a mandatory checksum recipe,
// unpack, verify — not Go code. Adding a tool is editing data. The embedded defaults ship
// pinned; operator overlay files (~/.iceclimber/catalog/*.yaml, config `catalog:`) extend
// or override by name. All fetching/verifying/caching is internal/relay; this package is
// only the parsed data + resolution to a concrete download for a sandbox fingerprint.
package catalog

import (
	"fmt"
	"sort"
	"strings"
)

// Kind is the fetch strategy for an entry (Go, parameterized by the entry's data).
type Kind string

const (
	// KindStaticPinned: a single binary/archive per platform with an inline URL + sha256
	// (the bash-static shape — zero network beyond the asset itself).
	KindStaticPinned Kind = "static-pinned"
	// KindGithubRelease: assets from a pinned GitHub release tag; checksums from the
	// release's own two-column sums file (recipe: shasums-file) or pinned inline (recipe:
	// pinned, for upstreams that publish no sums).
	KindGithubRelease Kind = "github-release"
	// KindShasumsIndex: a non-GitHub upstream exposing an index URL + a SHASUMS file.
	KindShasumsIndex Kind = "shasums-index"
)

// checksumRecipe names how an asset's sha256 is obtained. Mandatory: an entry with no
// verifiable recipe is rejected at load (the air-gap supply-chain floor).
type checksumRecipe string

const (
	// recipePinned: the sha256 is given inline per platform (the entry authored it).
	recipePinned checksumRecipe = "pinned"
	// recipeShasumsFile: a two-column "<sha256>  <filename>" file names it (fetched from
	// the release/index; the asset's basename keys the lookup).
	recipeShasumsFile checksumRecipe = "shasums-file"
)

// Entry is one tool definition (one item in a catalog file).
type Entry struct {
	Name        string              `yaml:"name"`
	Description string              `yaml:"description"`
	Kind        Kind                `yaml:"kind"`
	Version     string              `yaml:"version"`     // pinned (v1 has no floating "latest")
	Repo        string              `yaml:"repo"`        // github-release: "owner/name"
	ReleaseTag  string              `yaml:"release_tag"` // github-release: the tag when != version (e.g. jq's "jq-1.8.2")
	BaseURL     string              `yaml:"base_url"`    // shasums-index: the directory holding assets + the sums file
	Dest        string              `yaml:"dest"`        // install subdir under <root> (default "tools/<name>"); bash pins "shell/"
	Checksums   Checksums           `yaml:"checksums"`   // the recipe (+ shasums-file asset name)
	Platforms   map[string]Platform `yaml:"platforms"`   // "os/arch/libc" -> asset (+ inline sha for pinned)
	Unpack      Unpack              `yaml:"unpack"`
	Verify      string              `yaml:"verify"` // command run over the exec channel to prove it works
	source      string              // "embedded" or the overlay file path (set at load; not YAML)
}

// Checksums is an entry's checksum recipe.
type Checksums struct {
	Recipe checksumRecipe `yaml:"recipe"`
	Asset  string         `yaml:"asset"` // shasums-file: the sums file's asset/filename
}

// Platform is one row of the per-fingerprint table.
type Platform struct {
	Asset  string `yaml:"asset"`  // github-release/shasums-index: the asset filename
	URL    string `yaml:"url"`    // static-pinned: the full download URL
	SHA256 string `yaml:"sha256"` // pinned recipe: the asset's hex sha256
}

// Unpack describes how to turn the downloaded asset into installed binaries.
type Unpack struct {
	Format string   `yaml:"format"` // "binary" (single file) | "tar.gz" (strip-1 tree)
	Strip  int      `yaml:"strip"`  // tar.gz: leading path components to drop (informational; PushTarGz strips 1)
	Bins   []string `yaml:"bins"`   // basenames to link into <root>/tools/bin (and to chmod +x for a binary)
}

// platformKey builds the "os/arch/libc" lookup key from a probe fingerprint.
func platformKey(os, arch, libc string) string { return os + "/" + arch + "/" + libc }

// knownLibcs / knownOSes bound the platform vocabulary for validation (a typo'd key is a
// silent "no build for this box" otherwise).
var (
	knownOSes  = map[string]bool{"linux": true, "darwin": true}
	knownLibcs = map[string]bool{"musl": true, "glibc": true, "n/a": true, "": true}
)

// validate checks one entry is installable-by-construction: a known kind, a version, a
// mandatory + coherent checksum recipe, and at least one well-formed platform row. Errors
// name the entry + its source so an operator can fix the file.
func (e Entry) validate() error {
	where := fmt.Sprintf("tool %q (%s)", e.Name, e.source)
	if e.Name == "" {
		return fmt.Errorf("%s: missing name", where)
	}
	if e.Verify == "" {
		return fmt.Errorf("%s: missing verify command", where)
	}
	switch e.Kind {
	case KindStaticPinned, KindGithubRelease, KindShasumsIndex:
	default:
		return fmt.Errorf("%s: unknown kind %q (want static-pinned|github-release|shasums-index)", where, e.Kind)
	}
	if e.Version == "" {
		return fmt.Errorf("%s: missing version (v1 is pinned-only)", where)
	}
	// The checksum recipe is MANDATORY — the supply-chain floor.
	switch e.Checksums.Recipe {
	case recipePinned:
	case recipeShasumsFile:
		if e.Checksums.Asset == "" {
			return fmt.Errorf("%s: checksums.recipe shasums-file needs checksums.asset (the sums filename)", where)
		}
	case "":
		// static-pinned implies pinned (sha lives inline per platform); others must be explicit.
		if e.Kind != KindStaticPinned {
			return fmt.Errorf("%s: missing checksums.recipe (pinned|shasums-file) — an unverifiable tool is rejected", where)
		}
	default:
		return fmt.Errorf("%s: unknown checksums.recipe %q", where, e.Checksums.Recipe)
	}
	if e.Kind == KindGithubRelease && e.Repo == "" {
		return fmt.Errorf("%s: github-release needs repo (owner/name)", where)
	}
	if e.Kind == KindShasumsIndex && e.BaseURL == "" {
		return fmt.Errorf("%s: shasums-index needs base_url", where)
	}
	if len(e.Platforms) == 0 {
		return fmt.Errorf("%s: no platforms", where)
	}
	if e.Unpack.Format != "binary" && e.Unpack.Format != "tar.gz" {
		return fmt.Errorf("%s: unpack.format must be binary|tar.gz, got %q", where, e.Unpack.Format)
	}
	if len(e.Unpack.Bins) == 0 {
		return fmt.Errorf("%s: unpack.bins is empty (nothing to install)", where)
	}
	pinned := e.effectiveRecipe() == recipePinned
	for key, p := range e.Platforms {
		parts := strings.Split(key, "/")
		if len(parts) != 3 || !knownOSes[parts[0]] || !knownLibcs[parts[2]] {
			return fmt.Errorf("%s: bad platform key %q (want os/arch/libc, e.g. linux/x86_64/musl)", where, key)
		}
		if e.Kind == KindStaticPinned && p.URL == "" {
			return fmt.Errorf("%s: platform %q needs a url (static-pinned)", where, key)
		}
		if e.Kind != KindStaticPinned && p.Asset == "" {
			return fmt.Errorf("%s: platform %q needs an asset", where, key)
		}
		if pinned && p.SHA256 == "" {
			return fmt.Errorf("%s: platform %q needs a sha256 (pinned recipe)", where, key)
		}
	}
	return nil
}

// effectiveRecipe is the recipe with static-pinned's implicit "pinned" applied.
func (e Entry) effectiveRecipe() checksumRecipe {
	if e.Checksums.Recipe == "" && e.Kind == KindStaticPinned {
		return recipePinned
	}
	return e.Checksums.Recipe
}

// installDest is the entry's install directory under root ("tools/<name>" unless the
// entry pins a Dest, as bash does with "shell/").
func (e Entry) installDest(root string) string {
	d := e.Dest
	if d == "" {
		d = "tools/" + e.Name
	}
	return strings.TrimRight(root, "/") + "/" + strings.Trim(d, "/")
}

// Catalog is the merged, validated set of entries (operator overrides embedded by name).
type Catalog struct {
	byName map[string]Entry
}

// Names returns the tool names, sorted.
func (c *Catalog) Names() []string {
	out := make([]string, 0, len(c.byName))
	for n := range c.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Lookup returns the entry for a name.
func (c *Catalog) Lookup(name string) (Entry, bool) { e, ok := c.byName[name]; return e, ok }

// All returns every entry, sorted by name.
func (c *Catalog) All() []Entry {
	out := make([]Entry, 0, len(c.byName))
	for _, n := range c.Names() {
		out = append(out, c.byName[n])
	}
	return out
}

// Source reports where an entry came from ("embedded" or an overlay file path).
func (e Entry) Source() string { return e.source }
