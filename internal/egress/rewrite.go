package egress

import (
	"fmt"
	"net/url"
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

// normalizeURL ensures the URL has a path so a host rule like
// "https://host/*" matches a bare "https://host" (which has no path). Safe
// against "https://host.evil.com" because the rule's prefix includes the slash.
func normalizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

func hostOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("url has no host")
	}
	return u.Hostname(), nil
}

// HostGlob builds the host-level allow/deny rule for a URL: "<scheme>://<host>/*".
// Used by approve/deny when no explicit pattern is given (decision #31).
func HostGlob(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Scheme + "://" + u.Host + "/*"
}
