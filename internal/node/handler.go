package node

import (
	"context"
	"encoding/json"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

type installParams struct {
	Version string `json:"version"` // version line, e.g. "20" or "20.11"
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
		if p.Version == "" {
			return protocol.Errf(req.ID, "missing_version", `node.install requires params.version (e.g. "20")`)
		}
		res, err := inst.Install(ctx, p.Version)
		if err != nil {
			return protocol.Errf(req.ID, "install_failed", "%v", err)
		}
		return protocol.OK(req.ID, res)
	}
}
