package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/egress"
	"github.com/KuroKodaNinja/iceclimber/internal/proxy"
	"github.com/KuroKodaNinja/iceclimber/internal/webfetch"
)

type fakeApprover struct {
	choice webfetch.ApprovalChoice
	calls  int
}

func (f *fakeApprover) ApproveFetch(context.Context, webfetch.ApprovalPrompt) webfetch.ApprovalChoice {
	f.calls++
	return f.choice
}

func newTestPolicy(t *testing.T, allowed []egress.AllowedDomain, unlisted string) *egress.Policy {
	t.Helper()
	dir := t.TempDir()
	store := egress.NewStore(filepath.Join(dir, "approvals.json"), filepath.Join(dir, "pending.json"))
	return egress.NewPolicy(allowed, nil, unlisted, store)
}

func TestProxyPolicy_ConfigAllowed(t *testing.T) {
	pol := newTestPolicy(t, []egress.AllowedDomain{{Pattern: "pypi.org"}}, "gate")
	pp := newProxyPolicy(pol, nil, "s") // no approver
	v := pp.decide(proxy.Request{Method: "GET", Host: "pypi.org", Path: "/simple/six/", URL: "https://pypi.org/simple/six/"})
	if !v.Allow {
		t.Errorf("config-allowed host should be allowed without a prompt: %+v", v)
	}
}

func TestProxyPolicy_HoldDeniesWithoutApprover(t *testing.T) {
	pol := newTestPolicy(t, nil, "gate") // unlisted gates; no approver (headless)
	pp := newProxyPolicy(pol, nil, "s")
	v := pp.decide(proxy.Request{Host: "evil.test", URL: "https://evil.test/x"})
	if v.Allow {
		t.Error("unlisted host with no approver must be denied")
	}
}

func TestProxyPolicy_ApproveRememberPersistsAndCaches(t *testing.T) {
	pol := newTestPolicy(t, nil, "gate")
	ap := &fakeApprover{choice: webfetch.ApproveRemember}
	pp := newProxyPolicy(pol, ap, "s")

	req := proxy.Request{Host: "registry.npmjs.org", Path: "/blessed", URL: "https://registry.npmjs.org/blessed"}
	if v := pp.decide(req); !v.Allow {
		t.Fatalf("approve-remember should allow: %+v", v)
	}
	// A second request to the same host is cached — the operator is prompted at most once.
	req2 := proxy.Request{Host: "registry.npmjs.org", Path: "/blessed-contrib", URL: "https://registry.npmjs.org/blessed-contrib"}
	if v := pp.decide(req2); !v.Allow {
		t.Fatalf("second request should be allowed (cached): %+v", v)
	}
	if ap.calls != 1 {
		t.Errorf("approver called %d times, want 1 (per-host memo)", ap.calls)
	}
	// Approve-remember persisted a store rule (survives cache — a fresh policy allows).
	if pol.Decide("https://registry.npmjs.org/blessed") != egress.Allow {
		t.Error("approve-remember should have persisted an allow rule in the store")
	}
}

func TestProxyPolicy_DenyOnceCachedNoRepeatPrompt(t *testing.T) {
	pol := newTestPolicy(t, nil, "gate")
	ap := &fakeApprover{choice: webfetch.DenyOnce}
	pp := newProxyPolicy(pol, ap, "s")
	r := proxy.Request{Host: "blocked.test", URL: "https://blocked.test/x"}
	if v := pp.decide(r); v.Allow {
		t.Error("deny-once should deny")
	}
	if v := pp.decide(r); v.Allow { // cached deny
		t.Error("repeat should stay denied (cached)")
	}
	if ap.calls != 1 {
		t.Errorf("approver called %d times, want 1", ap.calls)
	}
}

func TestProxyPolicy_StoreDenyOverridesConfigAllow(t *testing.T) {
	pol := newTestPolicy(t, []egress.AllowedDomain{{Pattern: "pypi.org"}}, "gate")
	_ = pol.Store().AddDeny(egress.HostGlob("https://pypi.org/"))
	pp := newProxyPolicy(pol, nil, "s")
	if v := pp.decide(proxy.Request{Host: "pypi.org", URL: "https://pypi.org/simple/six/"}); v.Allow {
		t.Error("an explicit store Deny must override a config allow")
	}
}
