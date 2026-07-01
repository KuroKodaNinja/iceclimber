// Package egress is the web.fetch venue-routing and egress-gating policy (plan
// §6.1): which venue a URL uses, and — for the controller venue — whether a
// fetch is allowed, held for operator approval, or denied. The controller venue
// is a deliberate tunnel through the sandbox's isolation, so it ships only behind
// this gate.
package egress

// AllowedDomain maps a host pattern to the venue that can reach it.
type AllowedDomain struct {
	Pattern       string
	ReachableFrom string
}

// Rewrite redirects a matching URL and adopts a venue.
type Rewrite struct {
	Match     string
	RewriteTo string
	Venue     string
}

// Decision is the gate outcome for a controller-venue URL.
type Decision int

const (
	Allow Decision = iota
	Hold
	Deny
)

// String is the audit-friendly label.
func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Deny:
		return "denied"
	default:
		return "held"
	}
}

// Policy combines the operator's routing config with the rule store.
type Policy struct {
	allowed  []AllowedDomain
	rewrites []Rewrite
	unlisted string // "gate" | "deny"
	store    *Store
}

// NewPolicy builds a policy. An empty unlisted policy defaults to "gate".
func NewPolicy(allowed []AllowedDomain, rewrites []Rewrite, unlisted string, store *Store) *Policy {
	if unlisted == "" {
		unlisted = "gate"
	}
	return &Policy{allowed: allowed, rewrites: rewrites, unlisted: unlisted, store: store}
}

// Store exposes the rule/pending store (for the approve/deny CLI).
func (p *Policy) Store() *Store { return p.store }

// Resolve applies fetch rewrites then venue selection. It returns the
// (possibly rewritten) URL, the venue, and whether a rewrite fired.
func (p *Policy) Resolve(raw string) (url, venue string, rewritten bool, err error) {
	raw = normalizeURL(raw)
	for _, rw := range p.rewrites {
		if nu, ok := applyRewrite(rw, raw); ok {
			return nu, normVenue(rw.Venue), true, nil
		}
	}
	host, err := hostOf(raw)
	if err != nil {
		return "", "", false, err
	}
	for _, ad := range p.allowed {
		if globMatch(ad.Pattern, host) {
			return raw, normVenue(ad.ReachableFrom), false, nil
		}
	}
	return raw, VenueController, false, nil // unlisted → controller (gated)
}

// Decide gates a controller-venue URL: deny rules win, then allow rules, then the
// ConfigAllowed reports whether raw's host matches an operator-configured
// allowed_domains pattern. The proxy egress mode uses this as a pre-allow list — the
// operator declared these hosts reachable, so listed registries need no per-request
// approval (an explicit store Deny still overrides, since callers check Decide first).
func (p *Policy) ConfigAllowed(raw string) bool {
	host, err := hostOf(normalizeURL(raw))
	if err != nil {
		return false
	}
	for _, ad := range p.allowed {
		if globMatch(ad.Pattern, host) {
			return true
		}
	}
	return false
}

// StoreDenied reports whether url matches an explicit operator deny rule in the store —
// distinct from the unlisted-deny default (which Decide also reports as Deny). Proxy mode
// uses this so a config allowed_domains entry can pre-allow a host under
// unlisted_domain_policy: deny, while an explicit deny rule still wins.
func (p *Policy) StoreDenied(url string) bool {
	for _, r := range p.store.Deny() {
		if globMatch(r, url) {
			return true
		}
	}
	return false
}

// unlisted policy (gate → hold for approval, deny → refuse).
func (p *Policy) Decide(url string) Decision {
	for _, r := range p.store.Deny() {
		if globMatch(r, url) {
			return Deny
		}
	}
	for _, r := range p.store.Allow() {
		if globMatch(r, url) {
			return Allow
		}
	}
	if p.unlisted == "deny" {
		return Deny
	}
	return Hold
}

func normVenue(v string) string {
	if v == VenueSandbox {
		return VenueSandbox
	}
	return VenueController
}
