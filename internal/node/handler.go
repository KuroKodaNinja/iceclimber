package node

import (
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/runtime"
)

// RecommendedLine is the Node version line iceclimber installs when the caller doesn't ask
// for a specific one — the current, most-capable line, not a conservative older LTS (musl
// arm64 also needs Node ≥ 24). It is the single source of truth: the console's blank default,
// the `popo node.install` (no version / "latest") path, and NANA.md all resolve to it.
const RecommendedLine = "24"

// ResolveLine maps a caller's version request to a concrete line: an empty value or "latest"
// (case-insensitive) becomes RecommendedLine; anything else is used verbatim. This is what lets
// an agent install "the current version" without guessing — and stop picking an older one.
func ResolveLine(v string) string { return runtime.ResolveLine(RecommendedLine, v) }

// Spec binds Node into the shared install engine: its runtimes/<lang> subdir, its bin, its
// recommended line, how to prove it runs, and its live upstream resolver (Resolve).
func Spec() runtime.Spec {
	return runtime.Spec{
		Lang:        "node",
		BinName:     "node",
		Recommended: RecommendedLine,
		Verify: func(bin string) string {
			return remote.ShellQuote(bin) + " --version"
		},
		Resolve: Resolve,
	}
}
