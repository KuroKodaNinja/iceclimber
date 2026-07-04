package egress

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// Venue values.
const (
	VenueSandbox    = "sandbox"
	VenueController = "controller"
)

// applyRewrite returns the rewritten URL when raw matches the rule. A trailing
// '*' in Match captures the path tail, which is appended to RewriteTo's trailing
// '*' (plan §6.1).
func applyRewrite(rw Rewrite, raw string) (string, bool) {
	if !strings.HasSuffix(rw.Match, "*") {
		if raw == rw.Match {
			return rw.RewriteTo, true
		}
		return "", false
	}
	prefix := strings.TrimSuffix(rw.Match, "*")
	if !strings.HasPrefix(raw, prefix) {
		return "", false
	}
	tail := raw[len(prefix):]
	return strings.TrimSuffix(rw.RewriteTo, "*") + tail, true
}

// globMatch matches a pattern containing '*' wildcards against s. With no '*' it
// is an exact match.
func globMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	if !strings.HasSuffix(s, last) {
		return false
	}
	s = s[:len(s)-len(last)]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(s, mid)
		if i < 0 {
			return false
		}
		s = s[i+len(mid):]
	}
	return true
}

// Canonical canonicalizes a URL — or a full-URL glob pattern — into the one form the gate
// matches on, defending against the tricks an upstream silently collapses. Otherwise a deny
// like "https://pypi.org/simple/six/*" is evaded by "/simple/./six/", "/simple//six/", a cased
// host, or a ":443" port: the weak-normalized request misses the byte-exact deny, matches a
// broad allow, and the upstream then collapses the path and serves the denied resource.
//
// It yields: the scheme (defaulting https); a lower-cased, port-free, trailing-dot-free host;
// a path.Clean'd path (dot-segments + duplicate slashes collapsed, a trailing slash preserved
// so "/six/" still matches "/six/*"); and the original query. BOTH the concrete URL and the
// stored rule pass through it, so the two always agree. A parse failure returns the input
// unchanged, so matching fails closed at the call site rather than matching a broad allow.
func Canonical(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	host := canonHost(u)
	clean := path.Clean("/" + strings.TrimPrefix(u.Path, "/"))
	if clean == "." {
		clean = "/"
	}
	if strings.HasSuffix(u.Path, "/") && !strings.HasSuffix(clean, "/") {
		clean += "/" // path.Clean drops a trailing slash; keep it so "/x/" still matches "/x/*"
	}
	out := scheme + "://" + host + clean
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

// canonHost is the lower-cased, port-free, trailing-dot-free hostname (DNS is
// case-insensitive and "host." == "host"), the form host matching compares.
func canonHost(u *url.URL) string {
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

func hostOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("url has no host")
	}
	return canonHost(u), nil
}

// hostMatch matches an allowed_domains host pattern against an already-canonical (lower-cased)
// host, lower-casing the pattern so an operator's "PyPI.org" still matches "pypi.org".
func hostMatch(pattern, canonicalHost string) bool {
	return globMatch(strings.ToLower(pattern), canonicalHost)
}

// HostGlob builds the host-level allow/deny rule for a URL: "<scheme>://<host>/*", with the
// host canonicalized (lower-cased, port stripped) so the stored rule matches canonical request
// URLs. Used by approve/deny when no explicit pattern is given (decision #31).
func HostGlob(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + canonHost(u) + "/*"
}
