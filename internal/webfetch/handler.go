package webfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/audit"
	"github.com/KuroKodaNinja/iceclimber/internal/egress"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Deps are what a web.fetch needs: where to run, the egress policy, and the
// audit log.
type Deps struct {
	Runner remote.Runner
	FS     remotefs.FS
	Root   string
	Policy *egress.Policy
	Audit  *audit.Logger
}

// GateOutcome is the venue/gate result, neutral between the protocol handler and
// the CLI. Status is "ok" | "needs_clarification" | "denied".
type GateOutcome struct {
	Status    string
	Result    Result
	Venue     string
	URL       string // resolved (post-rewrite) URL
	Question  string // when needs_clarification
	PendingID string
}

// Run resolves the venue, applies the gate for the controller venue, performs the
// fetch when allowed, and audits the outcome. id is the request id (the held
// pending id for the protocol path; generated for the CLI when "").
func Run(ctx context.Context, d Deps, id string, req Request) (GateOutcome, error) {
	if id == "" {
		id = protocol.NewID() // CLI path: a stable id for the pending entry/approval
	}
	resolved, venue, rewritten, err := d.Policy.Resolve(req.URL)
	if err != nil {
		return GateOutcome{}, err
	}
	rewrittenURL := ""
	if rewritten {
		rewrittenURL = resolved
	}
	method := methodOrGet(req.Method)

	record := func(decision string, res Result, outcome string) {
		_ = d.Audit.Append(audit.Entry{
			ID: id, Type: "web.fetch", URL: req.URL, RewrittenURL: rewrittenURL,
			Method: method, Venue: venue, Decision: decision,
			StatusCode: res.StatusCode, BodySize: res.BodySize, BodySHA256: res.BodySHA256,
			Outcome: outcome,
		})
	}

	// Sandbox venue (incl. rewritten-to-mirror) is ungated — the sandbox's own
	// approved egress.
	if venue == egress.VenueSandbox {
		res, err := New(d.Runner, d.FS, d.Root).Fetch(ctx, withURL(req, resolved))
		if err != nil {
			record("allow", Result{}, "error")
			return GateOutcome{}, err
		}
		record("allow", res, "ok")
		return GateOutcome{Status: "ok", Result: res, Venue: venue, URL: resolved}, nil
	}

	// Controller venue → gate.
	switch d.Policy.Decide(resolved) {
	case egress.Deny:
		record("denied", Result{}, "error")
		return GateOutcome{Status: "denied", Venue: venue, URL: resolved}, nil

	case egress.Hold:
		host := hostOnly(resolved)
		_ = d.Policy.Store().AddPending(egress.PendingEntry{ID: id, URL: resolved, Host: host})
		record("held", Result{}, "ok")
		q := fmt.Sprintf("controller-venue fetch to %s requires approval; run: iceclimber approve %s", host, id)
		return GateOutcome{Status: "needs_clarification", Venue: venue, URL: resolved, Question: q, PendingID: id}, nil

	default: // Allow
		res, err := controllerFetch(ctx, d.FS, d.Root, method, req, resolved)
		if err != nil {
			record("allow", Result{}, "error")
			return GateOutcome{}, err
		}
		record("allow", res, "ok")
		return GateOutcome{Status: "ok", Result: res, Venue: venue, URL: resolved}, nil
	}
}

type fetchParams struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    *string           `json:"body"`
}

// Handler adapts Run into the web.fetch protocol handler.
func Handler(d Deps) protocol.Handler {
	return func(ctx context.Context, req protocol.Request) protocol.Response {
		var p fetchParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return protocol.Errf(req.ID, "malformed_params", "parse params: %v", err)
		}
		if p.URL == "" {
			return protocol.Errf(req.ID, "missing_url", "web.fetch requires params.url")
		}
		out, err := Run(ctx, d, req.ID, Request{URL: p.URL, Method: p.Method, Headers: p.Headers, Body: p.Body})
		if err != nil {
			return protocol.Errf(req.ID, "fetch_failed", "%v", err)
		}
		switch out.Status {
		case "needs_clarification":
			return protocol.NeedsClarification(req.ID, out.Question)
		case "denied":
			return protocol.Errf(req.ID, "egress_denied", "controller-venue fetch denied for %s", out.URL)
		default:
			return protocol.OK(req.ID, out.Result)
		}
	}
}

func withURL(req Request, url string) Request {
	req.URL = url
	return req
}

func methodOrGet(m string) string {
	if m == "" {
		return "GET"
	}
	return strings.ToUpper(m)
}

func hostOnly(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return raw
}
