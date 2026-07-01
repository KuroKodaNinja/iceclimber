package egress

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// IndexURLFromDenyGlob recognizes a pip "simple" package-index deny glob
// (`<scheme>://<host>/…/simple/<pkg>/*`) and returns the concrete index URL
// (`<scheme>://<host>/…/simple/<pkg>/`) to resolve the package's artifact URLs from. ok is
// false for anything else (a host glob, an all-of-simple glob, npm/maven paths). The host +
// path prefix come from the glob itself, so a custom index (not just pypi.org) is handled.
func IndexURLFromDenyGlob(glob string) (string, bool) {
	u, err := url.Parse(strings.TrimSuffix(glob, "*"))
	if err != nil || u.Host == "" {
		return "", false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, s := range segs {
		if s == "simple" {
			// require exactly one non-empty segment (the package) after "simple"
			if i == len(segs)-2 && segs[i+1] != "" {
				u.Path = "/" + strings.Join(segs[:i+2], "/") + "/"
				u.RawQuery, u.Fragment = "", ""
				return u.String(), true
			}
			return "", false
		}
	}
	return "", false
}

var hrefRe = regexp.MustCompile(`(?i)href\s*=\s*["']([^"']+)["']`)

// ParsePackageIndex extracts the absolute artifact URLs a pip "simple" index lists — from a
// PEP 691 JSON body (`application/vnd.pypi.simple.v1+json`) or a PEP 503 HTML body — each
// resolved against baseURL (the index URL) and stripped of its `#sha…` fragment (servers
// never see the fragment, so that's the URL the proxy will match). The listed URLs may point
// at ANY host — a CDN distinct from the index — which is precisely why binding a package
// deny to them closes the artifact bypass without assuming a specific CDN.
func ParsePackageIndex(body []byte, contentType, baseURL string) []string {
	base, _ := url.Parse(baseURL)
	resolve := func(ref string) string {
		ref = stripFragment(strings.TrimSpace(ref))
		if ref == "" {
			return ""
		}
		r, err := url.Parse(ref)
		if err != nil {
			return ""
		}
		if base != nil {
			r = base.ResolveReference(r)
		}
		if r.Host == "" {
			return ""
		}
		return r.String()
	}

	if strings.Contains(strings.ToLower(contentType), "json") {
		var doc struct {
			Files []struct {
				URL string `json:"url"`
			} `json:"files"`
		}
		if err := json.Unmarshal(body, &doc); err == nil && len(doc.Files) > 0 {
			seen := map[string]bool{}
			var out []string
			for _, f := range doc.Files {
				if u := resolve(f.URL); u != "" && !seen[u] {
					seen[u] = true
					out = append(out, u)
				}
			}
			return out
		}
		// fall through to HTML if the JSON didn't parse / had no files
	}

	seen := map[string]bool{}
	var out []string
	for _, m := range hrefRe.FindAllStringSubmatch(string(body), -1) {
		if u := resolve(m[1]); u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

func stripFragment(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}
