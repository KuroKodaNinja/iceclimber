package java

import (
	"github.com/KuroKodaNinja/iceclimber/internal/pins"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/runtime"
)

// RecommendedLine is the JDK feature version iceclimber installs when the caller doesn't ask
// for a specific one — the current recommended LTS. Pinned centrally in internal/pins.
var RecommendedLine = pins.Rt("java").RecommendedLine

// ResolveLine maps a caller's version request to a concrete feature version: an empty value or
// "latest" (case-insensitive) becomes RecommendedLine; anything else is used verbatim. External
// callers (the CLI, the console) share this so a blank/"latest" request lands on RecommendedLine.
func ResolveLine(v string) string { return runtime.ResolveLine(RecommendedLine, v) }

// Spec binds the JDK into the shared install engine: its runtimes/<lang> subdir, its bin, its
// recommended line, how to prove it runs, and its live upstream resolver (Resolve).
func Spec() runtime.Spec {
	return runtime.Spec{
		Lang:        "java",
		BinName:     "java",
		Recommended: RecommendedLine,
		Verify: func(bin string) string {
			return remote.ShellQuote(bin) + " -version"
		},
		Resolve: Resolve,
	}
}
