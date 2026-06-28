package java

import (
	"context"
	"encoding/json"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

type installParams struct {
	Version string `json:"version"` // feature version, e.g. "21" or "17"
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
		if p.Version == "" {
			return protocol.Errf(req.ID, "missing_version", `java.install requires params.version (e.g. "21")`)
		}
		res, err := inst.Install(ctx, p.Version)
		if err != nil {
			return protocol.Errf(req.ID, "install_failed", "%v", err)
		}
		return protocol.OK(req.ID, res)
	}
}
