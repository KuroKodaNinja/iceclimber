package protocol

import (
	"context"
	"time"
)

// Handler services one request type. It never returns an error: failures are
// encoded in the Response (status=error), which is what gets delivered.
type Handler func(ctx context.Context, req Request) Response

// Registry maps request types to handlers.
type Registry map[string]Handler

type pingResult struct {
	PongAt      time.Time `json:"pong_at"`
	PopoVersion string    `json:"popo_version"`
	// DispatchConcurrency is how many requests Popo services at once — declared here so the
	// agent can discover the limit deterministically (PROTOCOL.md "Concurrency") rather than
	// assume it. Omitted when unknown/1.
	DispatchConcurrency int `json:"dispatch_concurrency,omitempty"`
}

// PingHandler answers ping with pong, reporting the popo version and the dispatch concurrency
// limit (so the agent can discover how many requests it may run in parallel). The full registry
// is assembled at the composition root (cli), which owns the deps the heavier handlers need.
func PingHandler(version string, concurrency int) Handler {
	return func(_ context.Context, req Request) Response {
		return OK(req.ID, pingResult{PongAt: time.Now().UTC(), PopoVersion: version, DispatchConcurrency: concurrency})
	}
}
