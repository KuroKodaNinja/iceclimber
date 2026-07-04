package main

import (
	"os"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/cli"
)

// localVerbs are popo verbs handled entirely in-sandbox (no maildir round-trip to Popo), so they
// are NOT part of the agent↔controller protocol contract and aren't documented in PROTOCOL.md.
var localVerbs = map[string]bool{"collect": true, "shellenv": true, "log": true}

// TestPopoInstallVerbsCoverManagedLanguages enforces cross-surface parity: every managed runtime
// the controller can install must have a `<lang>.install` popo verb, so the in-sandbox agent can
// ask for any language the CLI/console can. Catches "added a runtime but not the agent verb".
func TestPopoInstallVerbsCoverManagedLanguages(t *testing.T) {
	for _, lang := range cli.ManagedLanguageNames() {
		v := lang + ".install"
		if _, ok := verbs[v]; !ok {
			t.Errorf("managed language %q has no popo verb %q — add it to cmd/popo's verbs map + the verbs.go builder", lang, v)
		}
	}
}

// TestPopoMaildirVerbsDocumented enforces the deterministic-documented-contract rule: every popo
// verb that crosses to Popo (a maildir verb) must appear in the agent-facing PROTOCOL.md verb
// table. Catches a verb added to popo but never documented for the agent — the doc-drift class of
// gap (e.g. a new verb, or a stale example) that only an audit otherwise surfaces.
func TestPopoMaildirVerbsDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../internal/skill/PROTOCOL.md")
	if err != nil {
		t.Fatalf("read PROTOCOL.md: %v", err)
	}
	text := string(doc)
	for v := range verbs {
		if localVerbs[v] {
			continue
		}
		// Require a VERB-TABLE ROW ("| `verb` | …"), not just any mention — a verb can appear in
		// prose (e.g. the concurrency note) while missing its contract row, which is the drift we
		// want to catch.
		if !strings.Contains(text, "| `"+v+"` |") {
			t.Errorf("popo maildir verb %q has no verb-table row in PROTOCOL.md (add one)", v)
		}
	}
}
