package egress

import (
	"fmt"
	"strings"
)

// ValidateHostPattern checks an allowed_domains pattern. Those are matched HOST-ONLY
// (ConfigAllowed/Resolve match against u.Hostname()), so a value carrying a scheme or path
// — e.g. "https://pypi.org/simple/*" — would silently never match. Reject that footgun up
// front: a host pattern is non-empty, whitespace-free, and has no scheme or path (a '*'
// wildcard for subdomains like "*.example.com" is fine).
func ValidateHostPattern(p string) error {
	switch {
	case strings.TrimSpace(p) == "":
		return fmt.Errorf("empty host pattern")
	case p != strings.TrimSpace(p) || strings.ContainsAny(p, " \t"):
		return fmt.Errorf("host pattern %q has whitespace", p)
	case strings.Contains(p, "://"):
		return fmt.Errorf("host pattern %q looks like a URL — allowed_domains match the host only (use e.g. %q, not a scheme/path)", p, hostPart(p))
	case strings.Contains(p, "/"):
		return fmt.Errorf("host pattern %q has a path — allowed_domains match the host only (path/package rules go in deny globs)", p)
	}
	return nil
}

// ValidateURLGlob checks a store allow/deny glob. Those are matched against the FULL URL
// (Decide/StoreDenied), so a bare host like "pypi.org" would never match "https://pypi.org/…".
// Require a scheme (or a leading '*'), non-empty, whitespace-free.
func ValidateURLGlob(p string) error {
	switch {
	case strings.TrimSpace(p) == "":
		return fmt.Errorf("empty rule")
	case p != strings.TrimSpace(p) || strings.ContainsAny(p, " \t"):
		return fmt.Errorf("rule %q has whitespace", p)
	case !strings.HasPrefix(p, "http://") && !strings.HasPrefix(p, "https://") && !strings.HasPrefix(p, "*"):
		return fmt.Errorf("rule %q must be a full-URL glob (e.g. %q) — store rules match the whole URL, not the host alone", p, "https://"+p+"/*")
	}
	return nil
}

// hostPart strips a scheme and any path from p, for a friendlier suggestion in the error.
func hostPart(p string) string {
	if i := strings.Index(p, "://"); i >= 0 {
		p = p[i+3:]
	}
	if i := strings.IndexByte(p, '/'); i >= 0 {
		p = p[:i]
	}
	return p
}
