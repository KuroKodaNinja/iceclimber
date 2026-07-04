package runtime

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// ResolveLine maps a caller's version request to a concrete line: an empty value or "latest"
// (case-insensitive) becomes recommended; anything else is used verbatim. This is what lets an
// agent install "the current version" without guessing — and stop picking an older one.
func ResolveLine(recommended, v string) string {
	s := strings.TrimSpace(v)
	if s == "" || strings.EqualFold(s, "latest") {
		return recommended
	}
	return s
}

// installParams is the <lang>.install request body (plan §4.2).
type installParams struct {
	Version string `json:"version"` // version line; "" / "latest" → the Spec's Recommended
}

// Handler adapts an Installer into its <lang>.install protocol handler.
func Handler(inst *Installer) protocol.Handler {
	return func(ctx context.Context, req protocol.Request) protocol.Response {
		var p installParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return protocol.Errf(req.ID, "malformed_params", "parse params: %v", err)
			}
		}
		res, err := inst.Install(ctx, ResolveLine(inst.spec.Recommended, p.Version))
		if err != nil {
			return protocol.Errf(req.ID, "install_failed", "%v", err)
		}
		return protocol.OK(req.ID, res)
	}
}
