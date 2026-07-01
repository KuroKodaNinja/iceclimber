package java

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// RecommendedLine is the JDK feature version iceclimber installs when the caller doesn't ask
// for a specific one — the current recommended LTS. Single source of truth for the console's
// blank default, the `popo java.install` (no version / "latest") path, and NANA.md.
const RecommendedLine = "21"

// ResolveLine maps a caller's version request to a concrete feature version: an empty value or
// "latest" (case-insensitive) becomes RecommendedLine; anything else is used verbatim.
func ResolveLine(v string) string {
	s := strings.TrimSpace(v)
	if s == "" || strings.EqualFold(s, "latest") {
		return RecommendedLine
	}
	return s
}

type installParams struct {
	Version string `json:"version"` // feature version, e.g. "21" or "17"; "" / "latest" → RecommendedLine
}

// Handler adapts the JDK installer into the java.install protocol handler.
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
