package egress

import (
	"path/filepath"
	"testing"
)

func TestIndexURLFromDenyGlob(t *testing.T) {
	ok := map[string]string{
		"https://pypi.org/simple/six/*":        "https://pypi.org/simple/six/",
		"https://pypi.org/simple/six/":         "https://pypi.org/simple/six/",
		"https://mycorp.dev/pypi/simple/six/*": "https://mycorp.dev/pypi/simple/six/", // custom index path prefix
	}
	for glob, want := range ok {
		if got, ok := IndexURLFromDenyGlob(glob); !ok || got != want {
			t.Errorf("IndexURLFromDenyGlob(%q) = %q,%v; want %q,true", glob, got, ok, want)
		}
	}
	notPkg := []string{
		"https://pypi.org/*",            // whole-host glob
		"https://pypi.org/simple/*",     // all packages, not one
		"https://pypi.org/simple/a/b/*", // too deep
		"https://files.pythonhosted.org/packages/*",
	}
	for _, glob := range notPkg {
		if _, ok := IndexURLFromDenyGlob(glob); ok {
			t.Errorf("IndexURLFromDenyGlob(%q) = ok; want not-ok", glob)
		}
	}
}

func TestParsePackageIndex(t *testing.T) {
	// PEP 691 JSON — artifacts on a DIFFERENT host than the index (the host-agnostic case),
	// with a #sha fragment that must be stripped.
	jsonBody := []byte(`{"meta":{"api-version":"1.0"},"name":"six","files":[` +
		`{"filename":"six-1.16.0-py2.py3-none-any.whl","url":"https://files.pythonhosted.org/packages/ab/cd/six-1.16.0-py2.py3-none-any.whl#sha256=deadbeef"},` +
		`{"filename":"six-1.16.0.tar.gz","url":"https://files.pythonhosted.org/packages/ef/01/six-1.16.0.tar.gz"}]}`)
	got := ParsePackageIndex(jsonBody, "application/vnd.pypi.simple.v1+json", "https://pypi.org/simple/six/")
	want := []string{
		"https://files.pythonhosted.org/packages/ab/cd/six-1.16.0-py2.py3-none-any.whl",
		"https://files.pythonhosted.org/packages/ef/01/six-1.16.0.tar.gz",
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("JSON parse = %v, want %v", got, want)
	}

	// PEP 503 HTML with a RELATIVE href — resolved against the index URL, fragment stripped.
	htmlBody := []byte(`<!DOCTYPE html><html><body>` +
		`<a href="../../packages/ab/cd/six-1.16.0-py2.py3-none-any.whl#sha256=deadbeef">six-1.16.0-py2.py3-none-any.whl</a>` +
		`</body></html>`)
	gotHTML := ParsePackageIndex(htmlBody, "text/html", "https://pypi.org/simple/six/")
	if len(gotHTML) != 1 || gotHTML[0] != "https://pypi.org/packages/ab/cd/six-1.16.0-py2.py3-none-any.whl" {
		t.Errorf("HTML parse = %v, want the resolved absolute artifact URL", gotHTML)
	}
}

func TestValidateHostPattern(t *testing.T) {
	ok := []string{"pypi.org", "files.pythonhosted.org", "*.example.com", "registry.npmjs.org"}
	for _, p := range ok {
		if err := ValidateHostPattern(p); err != nil {
			t.Errorf("ValidateHostPattern(%q) = %v, want nil", p, err)
		}
	}
	bad := []string{"", " pypi.org", "https://pypi.org", "pypi.org/simple/*", "https://pypi.org/simple/*"}
	for _, p := range bad {
		if err := ValidateHostPattern(p); err == nil {
			t.Errorf("ValidateHostPattern(%q) = nil, want an error (host-only patterns)", p)
		}
	}
}

func TestValidateURLGlob(t *testing.T) {
	ok := []string{"https://pypi.org/simple/six/*", "http://mirror.internal/*", "*://pypi.org/*", "*"}
	for _, p := range ok {
		if err := ValidateURLGlob(p); err != nil {
			t.Errorf("ValidateURLGlob(%q) = %v, want nil", p, err)
		}
	}
	bad := []string{"", "pypi.org", "pypi.org/simple/six/*", "  https://x/*"}
	for _, p := range bad {
		if err := ValidateURLGlob(p); err == nil {
			t.Errorf("ValidateURLGlob(%q) = nil, want an error (full-URL globs)", p)
		}
	}
}

func TestApplyRewrite(t *testing.T) {
	rw := Rewrite{Match: "https://repo1.maven.org/maven2/*", RewriteTo: "https://artifactory/maven-central/*", Venue: "sandbox"}
	got, ok := applyRewrite(rw, "https://repo1.maven.org/maven2/foo/bar.jar")
	if !ok || got != "https://artifactory/maven-central/foo/bar.jar" {
		t.Errorf("rewrite = %q, %v", got, ok)
	}
	if _, ok := applyRewrite(rw, "https://pypi.org/x"); ok {
		t.Error("non-matching URL should not rewrite")
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"https://example.com/*", "https://example.com/page", true},
		{"https://example.com/*", "https://other.com/page", false},
		{"*.example.com", "docs.example.com", true},
		{"docs.corp.internal", "docs.corp.internal", true},
		{"docs.corp.internal", "evil.com", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q,%q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

func TestResolve(t *testing.T) {
	p := NewPolicy(
		[]AllowedDomain{{Pattern: "docs.corp.internal", ReachableFrom: "sandbox"}},
		[]Rewrite{{Match: "https://pypi.org/*", RewriteTo: "https://mirror/pypi/*", Venue: "sandbox"}},
		"gate", nil,
	)
	// Rewrite fires → sandbox venue, rewritten.
	if u, v, rw, _ := p.Resolve("https://pypi.org/simple/six/"); u != "https://mirror/pypi/simple/six/" || v != VenueSandbox || !rw {
		t.Errorf("rewrite resolve = %q %q %v", u, v, rw)
	}
	// allowed_domains hit → its venue.
	if _, v, _, _ := p.Resolve("https://docs.corp.internal/x"); v != VenueSandbox {
		t.Errorf("allowed-domain venue = %q, want sandbox", v)
	}
	// Unlisted → controller.
	if _, v, _, _ := p.Resolve("https://example.com/x"); v != VenueController {
		t.Errorf("unlisted venue = %q, want controller", v)
	}
}

func TestDecide(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "approvals.json"), filepath.Join(dir, "pending.json"))
	mustNil(t, store.AddAllow("https://good.com/*"))
	mustNil(t, store.AddDeny("https://bad.com/*"))

	gate := NewPolicy(nil, nil, "gate", store)
	if d := gate.Decide("https://good.com/x"); d != Allow {
		t.Errorf("good = %v, want allow", d)
	}
	if d := gate.Decide("https://bad.com/x"); d != Deny {
		t.Errorf("bad = %v, want deny", d)
	}
	if d := gate.Decide("https://unknown.com/x"); d != Hold {
		t.Errorf("unknown (gate) = %v, want hold", d)
	}

	denyPolicy := NewPolicy(nil, nil, "deny", store)
	if d := denyPolicy.Decide("https://unknown.com/x"); d != Deny {
		t.Errorf("unknown (deny policy) = %v, want deny", d)
	}
}

func TestStorePendingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "approvals.json"), filepath.Join(dir, "pending.json"))
	mustNil(t, store.AddPending(PendingEntry{ID: "a", URL: "https://x.com/1", Host: "x.com"}))
	mustNil(t, store.AddPending(PendingEntry{ID: "a", URL: "https://x.com/1"})) // dedup by URL
	if len(store.Pending()) != 1 {
		t.Fatalf("pending = %d, want 1 (deduped)", len(store.Pending()))
	}
	e, ok, err := store.RemovePending("a")
	if err != nil || !ok || e.URL != "https://x.com/1" {
		t.Errorf("remove = %+v %v %v", e, ok, err)
	}
	if len(store.Pending()) != 0 {
		t.Error("pending should be empty after remove")
	}
}

// A host-level approval (https://host/*) must match a bare URL with no path
// (https://host) after normalization — the bug found in 6b functional testing.
func TestBareHostURLMatchesHostRule(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "a.json"), filepath.Join(dir, "p.json"))
	mustNil(t, store.AddAllow(HostGlob("https://example.com"))) // -> https://example.com/*
	p := NewPolicy(nil, nil, "gate", store)

	resolved, venue, _, err := p.Resolve("https://example.com")
	if err != nil || venue != VenueController {
		t.Fatalf("resolve = %q %q %v", resolved, venue, err)
	}
	if d := p.Decide(resolved); d != Allow {
		t.Errorf("bare-host URL not allowed by host rule: %v (resolved %q)", d, resolved)
	}
}

func TestHostGlob(t *testing.T) {
	if g := HostGlob("https://docs.python.org/3/library/"); g != "https://docs.python.org/*" {
		t.Errorf("HostGlob = %q", g)
	}
}

func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
