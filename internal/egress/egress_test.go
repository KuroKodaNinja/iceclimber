package egress

import (
	"path/filepath"
	"testing"
)

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
