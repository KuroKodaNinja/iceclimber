package cargo

import (
	"context"
	"encoding/json"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// buildParams is the cargo.build request body: the sandbox project dir (with a Cargo.toml), an
// optional Rust toolchain selector, and optional passthrough cargo args.
type buildParams struct {
	Project     string   `json:"project"`
	RustVersion string   `json:"rust_version,omitempty"`
	Args        []string `json:"args,omitempty"`
}

// BuildHandler adapts cargo.Build into the cargo.build protocol handler: a Cargo build of a
// sandbox project — online through the egress proxy in proxy mode, air-gapped (vendored) in
// relay mode.
func BuildHandler(d Deps) protocol.Handler {
	return func(ctx context.Context, req protocol.Request) protocol.Response {
		var p buildParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return protocol.Errf(req.ID, "malformed_params", "parse params: %v", err)
		}
		if p.Project == "" {
			return protocol.Errf(req.ID, "missing_project", "cargo.build requires params.project (a sandbox dir with Cargo.toml)")
		}
		res, err := Build(ctx, d, p.Project, p.RustVersion, p.Args)
		if err != nil {
			return protocol.Errf(req.ID, "build_failed", "%v", err)
		}
		return protocol.OK(req.ID, res)
	}
}
