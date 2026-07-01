package node

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// RecommendedLine is the Node version line iceclimber installs when the caller doesn't ask
// for a specific one — the current, most-capable line, not a conservative older LTS (musl
// arm64 also needs Node ≥ 24). It is the single source of truth: the console's blank default,
// the `popo node.install` (no version / "latest") path, and NANA.md all resolve to it.
const RecommendedLine = "24"

// ResolveLine maps a caller's version request to a concrete line: an empty value or "latest"
// (case-insensitive) becomes RecommendedLine; anything else is used verbatim. This is what lets
// an agent install "the current version" without guessing — and stop picking an older one.
func ResolveLine(v string) string {
	s := strings.TrimSpace(v)
	if s == "" || strings.EqualFold(s, "latest") {
		return RecommendedLine
	}
	return s
}

type installParams struct {
	Version string `json:"version"` // version line, e.g. "20" or "20.11"; "" / "latest" → RecommendedLine
}

// Handler adapts the Node installer into the node.install protocol handler.
func Handler(inst *Installer) protocol.Handler {
	return func(ctx context.Context, req protocol.Request) protocol.Response {
		var p installParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return protocol.Errf(req.ID, "malformed_params", "parse params: %v", err)
			}
		}
		res, err := inst.Install(ctx, ResolveLine(p.Version))
		if err != nil {
			return protocol.Errf(req.ID, "install_failed", "%v", err)
		}
		return protocol.OK(req.ID, res)
	}
}
