package webfetch

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/audit"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Deps are what a web.fetch needs: where to run, and the audit log.
type Deps struct {
	Runner remote.Runner
	FS     remotefs.FS
	Root   string
	Audit  *audit.Logger
}

// Run performs a fetch and records an audit entry (whatever the outcome). Shared
// by the CLI and the protocol handler. id is the request id for the audit line
// ("" for the synchronous CLI path).
func Run(ctx context.Context, d Deps, id string, req Request) (Result, error) {
	res, err := New(d.Runner, d.FS, d.Root).Fetch(ctx, req)
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	_ = d.Audit.Append(audit.Entry{
		ID:         id,
		Type:       "web.fetch",
		URL:        req.URL,
		Method:     methodOrGet(req.Method),
		Venue:      "sandbox-exec",
		StatusCode: res.StatusCode,
		BodySize:   res.BodySize,
		BodySHA256: res.BodySHA256,
		Outcome:    outcome,
	})
	return res, err
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
		res, err := Run(ctx, d, req.ID, Request{URL: p.URL, Method: p.Method, Headers: p.Headers, Body: p.Body})
		if err != nil {
			return protocol.Errf(req.ID, "fetch_failed", "%v", err)
		}
		return protocol.OK(req.ID, res)
	}
}

func methodOrGet(m string) string {
	if m == "" {
		return "GET"
	}
	return strings.ToUpper(m)
}
