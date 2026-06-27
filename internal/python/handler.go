package python

import (
	"context"
	"encoding/json"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// installParams is the python.install request body (plan §4.2).
type installParams struct {
	Version string `json:"version"` // minor version, e.g. "3.12"
}

// Handler adapts an Installer into the python.install protocol handler.
func Handler(inst *Installer) protocol.Handler {
	return func(ctx context.Context, req protocol.Request) protocol.Response {
		var p installParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return protocol.Errf(req.ID, "malformed_params", "parse params: %v", err)
			}
		}
		if p.Version == "" {
			return protocol.Errf(req.ID, "missing_version", `python.install requires params.version (e.g. "3.12")`)
		}
		res, err := inst.Install(ctx, p.Version)
		if err != nil {
			return protocol.Errf(req.ID, "install_failed", "%v", err)
		}
		return protocol.OK(req.ID, res)
	}
}
