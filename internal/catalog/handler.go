package catalog

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// installParams is the tool.install request body: just the tool name (the catalog + the
// sandbox fingerprint determine everything else).
type installParams struct {
	Name string `json:"name"`
}

// Handler adapts a catalog + install deps into the tool.install protocol handler. The
// catalog is loaded fresh per request via load (so an operator's mid-serve catalog edit is
// honored); deps carries the session bits (FS/runner/fingerprint/cache).
func Handler(load func() (*Catalog, error), deps InstallDeps) protocol.Handler {
	return func(ctx context.Context, req protocol.Request) protocol.Response {
		var p installParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return protocol.Errf(req.ID, "malformed_params", "parse params: %v", err)
			}
		}
		if p.Name == "" {
			return protocol.Errf(req.ID, "malformed_params", "tool.install needs a name")
		}
		cat, err := load()
		if err != nil {
			return protocol.Errf(req.ID, "catalog_error", "%v", err)
		}
		e, ok := cat.Lookup(p.Name)
		if !ok {
			return protocol.Errf(req.ID, "unknown_tool", "unknown tool %q (available: %s)", p.Name, joinNames(cat.Names()))
		}
		res, err := e.Install(ctx, deps)
		if err != nil {
			return protocol.Errf(req.ID, "install_failed", "%v", err)
		}
		return protocol.OK(req.ID, res)
	}
}

func joinNames(names []string) string {
	return strings.Join(names, ", ")
}
