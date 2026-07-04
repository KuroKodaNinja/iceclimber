package webfetch

import (
	"path/filepath"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/egress"
)

// TestControllerRedirectRegate exercises the controller-venue redirect re-gating the fix added
// (controller.go's CheckRedirect → handler.go's regateRedirect): a same-host redirect is
// followed, a cross-host redirect is refused unless the new host is explicitly allowed, and the
// SSRF floor is re-applied on every hop so an approved host can't 302 the fetch to a loopback /
// metadata address.
//
// It calls the SHIPPED regateRedirect directly rather than driving real HTTP: safeDialContext
// blocks the loopback address every httptest server binds, so client.Do can't reach a 127.0.0.1
// upstream to even observe a 302 — the decision logic is what matters and is tested here against
// a real egress.Policy.
func TestControllerRedirectRegate(t *testing.T) {
	dir := t.TempDir()
	store := egress.NewStore(filepath.Join(dir, "approvals.json"), filepath.Join(dir, "pending.json"))
	if err := store.AddAllow(egress.HostGlob("https://a.example/")); err != nil {
		t.Fatal(err)
	}
	policy := egress.NewPolicy(nil, nil, "gate", store)

	resolved := "https://a.example/pkg/"
	origHost := hostOnly(resolved)
	regate := func(hop string) error { return regateRedirect(policy, origHost, hop) }

	// Same-host redirect (path change only) is followed — the host was already gated.
	if err := regate("https://a.example/other/path"); err != nil {
		t.Errorf("same-host redirect refused: %v", err)
	}
	// A cross-host redirect to an UNAPPROVED host is refused (the core of the fix).
	if err := regate("https://b.evil/steal"); err == nil {
		t.Error("cross-host redirect to an unapproved host was allowed, want refused")
	}
	// Once host B is explicitly allowed, the cross-host redirect is followed.
	if err := store.AddAllow(egress.HostGlob("https://b.evil/")); err != nil {
		t.Fatal(err)
	}
	if err := regate("https://b.evil/steal"); err != nil {
		t.Errorf("approved cross-host redirect refused: %v", err)
	}
	// The SSRF floor still contains every hop regardless of policy: loopback + link-local metadata.
	if err := regate("http://127.0.0.1/latest/meta-data/"); err == nil {
		t.Error("redirect to loopback allowed, want an SSRF refusal")
	}
	if err := regate("http://169.254.169.254/"); err == nil {
		t.Error("redirect to link-local metadata allowed, want an SSRF refusal")
	}
}
