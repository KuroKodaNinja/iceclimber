package python

import (
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/runtime"
)

// RecommendedLine is the Python minor version iceclimber installs when the caller doesn't ask
// for a specific one — the current recommended line. Single source of truth for the console's
// blank default, the `popo python.install` (no version / "latest") path, and NANA.md.
const RecommendedLine = "3.12"

// ResolveLine maps a caller's version request to a concrete minor: an empty value or "latest"
// (case-insensitive) becomes RecommendedLine; anything else is used verbatim. External callers
// (the CLI, the console) share this so a blank/"latest" request always lands on RecommendedLine.
func ResolveLine(v string) string { return runtime.ResolveLine(RecommendedLine, v) }

// Spec binds Python into the shared install engine: its runtimes/<lang> subdir, its bin, its
// recommended line, how to prove it runs, and its live upstream resolver (Resolve).
func Spec() runtime.Spec {
	return runtime.Spec{
		Lang:        "python",
		BinName:     "python3",
		Recommended: RecommendedLine,
		Verify: func(bin string) string {
			return remote.ShellQuote(bin) + " -c " + remote.ShellQuote("import sys; print(sys.version)")
		},
		Resolve: Resolve,
	}
}
