package cli

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

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

func TestProxyPolicy_ConfigAllowBeatsUnlistedDeny(t *testing.T) {
	// unlisted_domain_policy: deny — a config allowed_domains entry must still be allowed
	// (regression for the bug where Decide returned Deny before ConfigAllowed was checked).
	pol := newTestPolicy(t, []egress.AllowedDomain{{Pattern: "pypi.org"}}, "deny")
	pp := newProxyPolicy(pol, nil, "s")
	if v := pp.decide(proxy.Request{Host: "pypi.org", URL: "https://pypi.org/simple/six/"}); !v.Allow {
		t.Error("config-allowed host must be allowed even under unlisted_domain_policy: deny")
	}
	if v := pp.decide(proxy.Request{Host: "evil.test", URL: "https://evil.test/"}); v.Allow {
		t.Error("unlisted host under deny policy must be denied")
	}
}

func TestProxyPolicy_Rewrite(t *testing.T) {
	dir := t.TempDir()
	store := egress.NewStore(dir+"/a.json", dir+"/p.json")
	pol := egress.NewPolicy(
		[]egress.AllowedDomain{{Pattern: "mirror.internal"}},
		[]egress.Rewrite{{Match: "https://pypi.org/*", RewriteTo: "https://mirror.internal/*"}},
		"gate", store)
	pp := newProxyPolicy(pol, nil, "s")
	v := pp.decide(proxy.Request{Host: "pypi.org", Path: "/simple/six/", URL: "https://pypi.org/simple/six/"})
	if !v.Allow || v.RewriteHost != "mirror.internal" {
		t.Errorf("rewrite verdict = %+v, want allow + RewriteHost mirror.internal", v)
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

func TestListenWithRetry(t *testing.T) {
	denied := errors.New("ssh: tcpip-forward request denied by peer")

	// Transient: fails with the forward-denied race twice, then succeeds — retry absorbs it.
	t.Run("retries the forward-denied race then succeeds", func(t *testing.T) {
		calls, slept := 0, 0
		ln, err := listenWithRetry(func() (net.Listener, error) {
			calls++
			if calls < 3 {
				return nil, denied
			}
			return fakeListener{}, nil
		}, 5, func(time.Duration) { slept++ })
		if err != nil || ln == nil {
			t.Fatalf("expected success after retries, got ln=%v err=%v", ln, err)
		}
		if calls != 3 || slept != 2 {
			t.Errorf("calls=%d slept=%d, want 3 and 2", calls, slept)
		}
	})

	// Persistent: keeps returning forward-denied — exhausts attempts, returns the last error.
	t.Run("exhausts on a persistently forwarded port", func(t *testing.T) {
		calls := 0
		_, err := listenWithRetry(func() (net.Listener, error) { calls++; return nil, denied }, 5, func(time.Duration) {})
		if !isForwardDenied(err) {
			t.Errorf("err = %v, want a forward-denied error", err)
		}
		if calls != 5 {
			t.Errorf("calls = %d, want 5 (all attempts)", calls)
		}
	})

	// A non-race error fails fast (no retry, no sleep).
	t.Run("fails fast on an unrelated error", func(t *testing.T) {
		calls, slept := 0, 0
		other := errors.New("connection reset")
		_, err := listenWithRetry(func() (net.Listener, error) { calls++; return nil, other }, 5, func(time.Duration) { slept++ })
		if !errors.Is(err, other) || calls != 1 || slept != 0 {
			t.Errorf("want fail-fast (1 call, 0 sleeps, same err); got calls=%d slept=%d err=%v", calls, slept, err)
		}
	})
}

// fakeListener is a no-op net.Listener for the retry test (never accepts).
type fakeListener struct{}

func (fakeListener) Accept() (net.Conn, error) { return nil, errors.New("closed") }
func (fakeListener) Close() error              { return nil }
func (fakeListener) Addr() net.Addr            { return nil }

func TestProxyPolicy_ArtifactDeny(t *testing.T) {
	// (a) Startup resolve: a denied pip index seeds its artifacts as denied — host-agnostic,
	// the artifact lives on a DIFFERENT host than the index.
	pol := newTestPolicy(t, nil, "gate")
	if err := pol.Store().AddDeny("https://pypi.org/simple/six/*"); err != nil {
		t.Fatal(err)
	}
	pp := newProxyPolicy(pol, nil, "s")
	fixture := []byte(`{"files":[{"url":"https://files.pythonhosted.org/packages/ab/cd/six-1.16.0-py3-none-any.whl#sha256=x"}]}`)
	pp.seedDeniedArtifacts(context.Background(), func(_ context.Context, u string) ([]byte, string, error) {
		if u != "https://pypi.org/simple/six/" {
			t.Errorf("resolve fetched %q, want the index URL", u)
		}
		return fixture, "application/vnd.pypi.simple.v1+json", nil
	})
	if !pp.artifactDenied(normalizeEgressURL("https://files.pythonhosted.org/packages/ab/cd/six-1.16.0-py3-none-any.whl")) {
		t.Error("a resolved artifact of a denied package must be denied (H1 close)")
	}
	if pp.artifactDenied(normalizeEgressURL("https://files.pythonhosted.org/packages/zz/idna-3.7.whl")) {
		t.Error("an unrelated artifact must not be denied")
	}

	// (b) Learn-on-serve: an allowed package's index is learned; denying the package later
	// blocks its already-seen artifact — no reconnect needed.
	pol2 := newTestPolicy(t, nil, "gate")
	pp2 := newProxyPolicy(pol2, nil, "s")
	pp2.recordIndex("https://pypi.org/simple/leftpad/", []string{"https://cdn.example.com/x/leftpad-1.0.whl"})
	art := normalizeEgressURL("https://cdn.example.com/x/leftpad-1.0.whl")
	if pp2.artifactDenied(art) {
		t.Error("a learned artifact must NOT be denied until its package is denied")
	}
	if err := pol2.Store().AddDeny("https://pypi.org/simple/leftpad/*"); err != nil {
		t.Fatal(err)
	}
	if !pp2.artifactDenied(art) {
		t.Error("once the package index is denied, its learned artifact must be denied")
	}
}

// TestPathDenyURL locks in the normalization that stops a package/path deny glob from being
// evaded by tricks an upstream silently collapses: the :443 port, dot-segments, duplicate
// slashes, a decoded dot (arrives decoded in Path), and a preserved trailing slash so
// "/six/" still matches "/six/*". Host is already canonicalized upstream (proxy.canonHost).
func TestPathDenyURL(t *testing.T) {
	cases := []struct {
		name       string
		host, path string
		url        string
		want       string
	}{
		{"port stripped", "pypi.org", "/simple/six/", "https://pypi.org:443/simple/six/", "https://pypi.org/simple/six/"},
		{"dot-segment", "pypi.org", "/simple/./six/", "https://pypi.org:443/simple/./six/", "https://pypi.org/simple/six/"},
		{"double slash", "pypi.org", "/simple//six/", "https://pypi.org/simple//six/", "https://pypi.org/simple/six/"},
		{"parent traversal", "pypi.org", "/simple/x/../six/", "https://pypi.org/simple/x/../six/", "https://pypi.org/simple/six/"},
		{"no trailing slash preserved", "pypi.org", "/simple/six", "https://pypi.org/simple/six", "https://pypi.org/simple/six"},
		{"query kept", "pypi.org", "/a/b/", "https://pypi.org/a/b/?k=v", "https://pypi.org/a/b/?k=v"},
		{"http scheme kept", "mirror.internal", "/x/", "http://mirror.internal/x/", "http://mirror.internal/x/"},
		{"root path", "pypi.org", "/", "https://pypi.org/", "https://pypi.org/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pathDenyURL(proxy.Request{Host: c.host, Path: c.path, URL: c.url}); got != c.want {
				t.Errorf("pathDenyURL(host=%q path=%q url=%q) = %q, want %q", c.host, c.path, c.url, got, c.want)
			}
		})
	}
}
