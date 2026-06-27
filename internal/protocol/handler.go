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
}

// PingHandler answers ping with pong, reporting version (plan §4.1). The full
// registry is assembled at the composition root (cli), which owns the deps the
// heavier handlers need.
func PingHandler(version string) Handler {
	return func(_ context.Context, req Request) Response {
		return OK(req.ID, pingResult{PongAt: time.Now().UTC(), PopoVersion: version})
	}
}
