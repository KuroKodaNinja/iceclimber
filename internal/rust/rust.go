package rust

import (
	"github.com/KuroKodaNinja/iceclimber/internal/pins"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/runtime"
)

// RecommendedLine is the Rust line iceclimber installs when the caller doesn't ask for a
// specific one — the current stable toolchain (resolved to a concrete version at install).
// Pinned centrally in internal/pins.
var RecommendedLine = pins.Rt("rust").RecommendedLine

// ResolveLine maps a caller's version request to a concrete line: an empty value or "latest"
// (case-insensitive) becomes RecommendedLine ("stable"); anything else is used verbatim (an
// exact "1.83.0", or "stable"). The upstream resolver turns "stable" into a concrete version.
func ResolveLine(v string) string { return runtime.ResolveLine(RecommendedLine, v) }

// Spec binds Rust into the shared install engine. It is the first Spec to use Assemble: Rust's
// component-split tarball is merged into one prefix on the controller before the push.
func Spec() runtime.Spec {
	return runtime.Spec{
		Lang:        "rust",
		BinName:     "rustc",
		Recommended: RecommendedLine,
		Verify:      func(bin string) string { return remote.ShellQuote(bin) + " --version" },
		Resolve:     Resolve,
		Assemble:    Assemble,
	}
}
