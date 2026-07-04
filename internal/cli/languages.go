package cli

import (
	"github.com/KuroKodaNinja/iceclimber/internal/java"
	"github.com/KuroKodaNinja/iceclimber/internal/node"
	"github.com/KuroKodaNinja/iceclimber/internal/python"
	"github.com/KuroKodaNinja/iceclimber/internal/runtime"
	"github.com/KuroKodaNinja/iceclimber/internal/rust"
)

// managedLangs is the registry of iceclimber-managed language runtimes — the single place a new
// language binds its install-engine Spec (bin name, recommended line, verify command, upstream
// resolver). Everything that used to switch on a language name — installer construction, the
// per-language <lang>.install handlers, the console's default version, and the status runtime
// probe — reads this list instead, so adding a language is one entry here plus its own
// resolve.go/Spec, not edits scattered across a handful of switch statements.
func managedLangs() []runtime.Spec {
	return []runtime.Spec{python.Spec(), node.Spec(), java.Spec(), rust.Spec()}
}

// managedLangSpec looks up a registered language's Spec by its canonical name ("python",
// "node", "java", …). ok=false for an unknown/unmanaged name.
func managedLangSpec(name string) (runtime.Spec, bool) {
	for _, s := range managedLangs() {
		if s.Lang == name {
			return s, true
		}
	}
	return runtime.Spec{}, false
}

// canonicalLang maps a UI/label language to its canonical registry name — the JS runtime is
// labelled "javascript" but registered as "node"; every other name maps to itself.
func canonicalLang(label string) string {
	if label == "javascript" {
		return "node"
	}
	return label
}
