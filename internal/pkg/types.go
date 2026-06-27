// Package pkg holds the manager-neutral vocabulary for package installation:
// the request specs, a resolved plan, and the per-package outcome. Python's pip
// is the first implementation (internal/pip); the same shape (resolve → retrieve)
// generalizes to other languages' managers (plan §4.3, §5). A Manager interface
// can be extracted here once a second manager exists — not built speculatively.
package pkg

// Spec is a requested package. An empty Version means "unversioned" — resolved
// by the package manager's own default (no forced pinning at our layer).
type Spec struct {
	Name    string
	Version string
}

// Resolved is one package the manager's resolver pinned, with its artifact URL
// and hash. Recording these gives determinism even when the request was
// unversioned.
type Resolved struct {
	Name    string
	Version string
	URL     string
	SHA256  string
}

// Plan is the co-resolved set produced by the resolve stage (every package,
// including transitive dependencies).
type Plan struct {
	Packages []Resolved
}

// Resolution tiers (plan §5): how a package was obtained.
const (
	TierMirror = "mirror" // Tier 0: installed directly from the internal mirror
)

// Installed records one successfully installed package.
type Installed struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Tier    string `json:"tier"`
	SHA256  string `json:"sha256,omitempty"`
}

// Failure records one package that could not be retrieved/installed.
type Failure struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error"`
}

// Outcome is the per-package result of the retrieve/install stage. A request is
// "ok" even if some packages failed (plan §4.3) — the failures live here.
type Outcome struct {
	Installed []Installed `json:"installed"`
	Failed    []Failure   `json:"failed"`
}
