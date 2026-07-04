package catalog

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

//go:embed catalog.yaml
var embeddedYAML []byte

// parseFile decodes a catalog YAML document (a list of entries), stamping each entry's
// source for error messages + provenance display.
func parseFile(data []byte, source string) ([]Entry, error) {
	var entries []Entry
	if err := yaml.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse catalog %s: %w", source, err)
	}
	for i := range entries {
		entries[i].source = source
	}
	return entries, nil
}

// Load builds the merged, validated catalog: the embedded defaults first, then each
// operator overlay path in order. Within one source a duplicate name is a hard error
// (naming both would-be definitions); across sources a later entry OVERRIDES an earlier
// one by name (operator over embedded — the documented policy), silently. Every entry is
// validated; a broken operator entry is an error that names the file, and never takes the
// embedded set down with it (the caller decides whether to surface or continue).
//
// overlayPaths are files and/or directories: a directory contributes its *.yaml children
// (sorted), a file contributes itself. Missing paths are skipped (an operator may point at
// a catalog dir they haven't created yet).
func Load(overlayPaths []string) (*Catalog, error) {
	sources := []struct {
		data   []byte
		source string
	}{{embeddedYAML, "embedded"}}

	for _, p := range expandCatalogPaths(overlayPaths) {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read catalog %s: %w", p, err)
		}
		sources = append(sources, struct {
			data   []byte
			source string
		}{data, p})
	}

	byName := map[string]Entry{}
	for _, src := range sources {
		entries, err := parseFile(src.data, src.source)
		if err != nil {
			return nil, err
		}
		seenInFile := map[string]bool{}
		for _, e := range entries {
			if err := e.validate(); err != nil {
				return nil, err
			}
			if seenInFile[e.Name] {
				return nil, fmt.Errorf("tool %q defined twice in %s", e.Name, src.source)
			}
			seenInFile[e.Name] = true
			byName[e.Name] = e // later source overrides (operator > embedded)
		}
	}
	// Bin-name uniqueness across the FINAL merged set: two tools linking the same name
	// into tools/bin would clobber each other. Checked on the resolved catalog (after
	// overrides) so an operator override that changes a tool's bins is judged correctly.
	binOwner := map[string]string{}
	for _, name := range sortedKeys(byName) {
		for _, b := range byName[name].Unpack.Bins {
			if other, dup := binOwner[b]; dup {
				return nil, fmt.Errorf("tools %q and %q both provide the binary %q — they can't share the %s PATH dir; override or remove one", other, name, b, "tools/bin")
			}
			binOwner[b] = name
		}
	}
	return &Catalog{byName: byName}, nil
}

func sortedKeys(m map[string]Entry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// expandCatalogPaths turns the configured paths into concrete files: a directory yields
// its sorted *.yaml children; a file yields itself; a missing path is dropped.
func expandCatalogPaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			out = append(out, p)
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(p, "*.yaml"))
		sort.Strings(matches)
		out = append(out, matches...)
	}
	return out
}

// DefaultOverlayDir is the conventional operator catalog directory (~/.iceclimber/catalog).
func DefaultOverlayDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".iceclimber", "catalog")
}
