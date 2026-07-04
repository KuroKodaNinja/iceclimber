package egress

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestCanonical pins the canonicalizer's normalization so the gate and the request-time denier
// agree on what a URL "is".
func TestCanonical(t *testing.T) {
	cases := map[string]string{
		"https://Host.COM/a":            "https://host.com/a",
		"https://host.com:443/a":        "https://host.com/a",
		"https://host.com./a":           "https://host.com/a",
		"https://host.com/a/./b":        "https://host.com/a/b",
		"https://host.com/a//b":         "https://host.com/a/b",
		"https://host.com/a/../b":       "https://host.com/b",
		"https://host.com/x/":           "https://host.com/x/", // trailing slash preserved
		"https://host.com":              "https://host.com/",   // bare host gets a path
		"https://host.com/p?q=1&r=2":    "https://host.com/p?q=1&r=2",
		"https://host.com/simple/./six": "https://host.com/simple/six",
	}
	for in, want := range cases {
		if got := Canonical(in); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDenyEvasionCanonicalized is the P0 regression: a deny rule must not be evadable by the
// path/host tricks an upstream silently collapses. Both a host-level deny and a path-level deny
// (under a broad allow) must hold against cased hosts, ":443", dot-segments, and double slashes.
func TestDenyEvasionCanonicalized(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "approvals.json"), filepath.Join(dir, "pending.json"))
	mustNil(t, store.AddAllow("https://reg.com/*"))
	mustNil(t, store.AddDeny("https://reg.com/pkgs/evil/*"))
	mustNil(t, store.AddDeny("https://evil.com/*"))
	p := NewPolicy(nil, nil, "gate", store)

	denied := []string{
		"https://evil.com/x",
		"https://EVIL.com/x",            // cased host
		"https://evil.com:443/x",        // default port
		"https://evil.com./x",           // trailing dot
		"https://reg.com/pkgs/evil/x",   // the plain path deny
		"https://reg.com/pkgs/./evil/x", // dot-segment
		"https://reg.com/pkgs//evil/x",  // double slash
		"https://reg.com/pkgs/a/../evil/x",
	}
	for _, u := range denied {
		if d := p.Decide(u); d != Deny {
			t.Errorf("Decide(%q) = %v, want Deny (deny rule evaded)", u, d)
		}
	}
	// A genuinely different path under the allow is still allowed (the deny isn't over-broad).
	if d := p.Decide("https://reg.com/pkgs/good/x"); d != Allow {
		t.Errorf("Decide(good) = %v, want Allow", d)
	}
}

// TestConfigAllowedCaseInsensitive: an operator allowed_domains pattern matches regardless of
// host case (DNS is case-insensitive).
func TestConfigAllowedCaseInsensitive(t *testing.T) {
	p := NewPolicy([]AllowedDomain{{Pattern: "PyPI.org", ReachableFrom: VenueController}}, nil, "gate", nil)
	for _, u := range []string{"https://pypi.org/x", "https://PyPI.ORG/x", "https://pypi.org:443/x"} {
		if !p.ConfigAllowed(u) {
			t.Errorf("ConfigAllowed(%q) = false, want true", u)
		}
	}
}

// TestStoreConcurrentWriters: parallel mutations (the parallel-dispatch reality, decision #84)
// must not lose an update. With N goroutines each adding a distinct rule + pending entry, all N
// must survive — the in-process mutex + atomic write guarantee it (run under -race).
func TestStoreConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "approvals.json"), filepath.Join(dir, "pending.json"))
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mustNil(t, store.AddAllow(fmt.Sprintf("https://h%d.com/*", i)))
			mustNil(t, store.AddPending(PendingEntry{ID: fmt.Sprintf("id%d", i), URL: fmt.Sprintf("https://h%d.com/p", i)}))
		}(i)
	}
	wg.Wait()
	if got := len(store.Allow()); got != n {
		t.Errorf("allow rules = %d, want %d (lost updates)", got, n)
	}
	if got := len(store.Pending()); got != n {
		t.Errorf("pending = %d, want %d (lost updates)", got, n)
	}
}

// TestStoreFailClosedOnCorrupt: a corrupt approvals file must NOT read as "no rules" (which
// would silently drop a deny). The gate denies, and a mutation aborts rather than overwriting
// (which would wipe the operator's rules).
func TestStoreFailClosedOnCorrupt(t *testing.T) {
	dir := t.TempDir()
	ap := filepath.Join(dir, "approvals.json")
	if err := os.WriteFile(ap, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewStore(ap, filepath.Join(dir, "pending.json"))
	p := NewPolicy(nil, nil, "gate", store)

	if d := p.Decide("https://anything.com/x"); d != Deny {
		t.Errorf("Decide on corrupt store = %v, want Deny (fail closed)", d)
	}
	if !p.StoreDenied("https://anything.com/x") {
		t.Error("StoreDenied on corrupt store = false, want true (fail closed)")
	}
	if err := store.AddAllow("https://ok.com/*"); err == nil {
		t.Error("AddAllow over a corrupt file should error, not wipe the existing rules")
	}
	// The corrupt bytes are untouched (not overwritten to empty).
	if b, _ := os.ReadFile(ap); string(b) != "{ this is not json" {
		t.Errorf("corrupt file was modified: %q", b)
	}
}

// TestStoreAtomicNoPartialRead: a completed write is valid JSON (temp+rename), so a reader never
// sees a truncated file — asserted here by round-tripping a written file through json.Unmarshal.
func TestStoreAtomicNoPartialRead(t *testing.T) {
	dir := t.TempDir()
	ap := filepath.Join(dir, "approvals.json")
	store := NewStore(ap, filepath.Join(dir, "pending.json"))
	mustNil(t, store.AddAllow("https://a.com/*"))
	b, err := os.ReadFile(ap)
	if err != nil {
		t.Fatal(err)
	}
	var r rules
	if err := json.Unmarshal(b, &r); err != nil {
		t.Errorf("written approvals file is not valid JSON: %v", err)
	}
}
