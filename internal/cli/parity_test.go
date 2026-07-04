package cli

import (
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/tui"
)

// TestManagedLanguageSurfaceParity enforces the mandatory TUI/cmdline-parity rule as a test
// rather than a convention: every managed runtime the controller can install (managedLangs) must
// be reachable from BOTH the CLI (`install <lang>`) AND the console install form
// (tui.RuntimeInstallLanguages). This is the check that would have caught Rust shipping on the
// CLI + agent verbs but not the TUI — a class of gap that recurred precisely because it was a
// remembered rule, not an enforced one.
func TestManagedLanguageSurfaceParity(t *testing.T) {
	// The CLI install subcommands, by name.
	installCmds := map[string]bool{}
	for _, c := range newInstallCmd().Commands() {
		installCmds[c.Name()] = true
	}
	// The console install-form runtime options, mapped to their canonical runtime name.
	tuiLangs := map[string]bool{}
	for _, rl := range tui.RuntimeInstallLanguages() {
		tuiLangs[canonicalLang(rl.Value)] = true
	}

	for _, lang := range ManagedLanguageNames() {
		if !installCmds[lang] {
			t.Errorf("managed language %q has no `install %s` CLI command — add it to newInstallCmd()", lang, lang)
		}
		if !tuiLangs[lang] {
			t.Errorf("managed language %q is missing from the console install form — add it to tui.RuntimeInstallLanguages()", lang)
		}
	}
	// And the reverse: a runtime option in the form must be a real managed language (no dangling
	// option the operator can pick but the controller can't install).
	managed := map[string]bool{}
	for _, lang := range ManagedLanguageNames() {
		managed[lang] = true
	}
	for _, rl := range tui.RuntimeInstallLanguages() {
		if !managed[canonicalLang(rl.Value)] {
			t.Errorf("console install form offers %q, which is not a managed language", rl.Value)
		}
	}
}
