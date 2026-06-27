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

// DefaultRegistry returns the handlers built into Popo. version is reported by
// ping. Phase 2 registers only ping; later phases add python.install etc.
func DefaultRegistry(version string) Registry {
	return Registry{
		"ping": pingHandler(version),
	}
}

type pingResult struct {
	PongAt      time.Time `json:"pong_at"`
	PopoVersion string    `json:"popo_version"`
}

func pingHandler(version string) Handler {
	return func(_ context.Context, req Request) Response {
		return ok(req.ID, pingResult{PongAt: time.Now().UTC(), PopoVersion: version})
	}
}
