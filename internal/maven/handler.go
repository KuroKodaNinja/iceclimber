package maven

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"

	"github.com/KuroKodaNinja/iceclimber/internal/java"
	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Deps are what a maven.install needs from the session.
type Deps struct {
	FS         remotefs.FS
	Runner     remote.Runner
	Root       string
	Arch       string
	Libc       string
	MirrorURL  string // Tier-0 Maven repository; empty = Maven Central
	CacheDir   string
	HTTPClient *http.Client
}

// Result is the maven.install response body: the resolved coordinates plus the
// CLASSPATH the agent puts on `java -cp` to use them (analogous to npm's NODE_PATH).
type Result struct {
	Installed []pkg.Installed `json:"installed"`
	Failed    []pkg.Failure   `json:"failed"`
	Classpath string          `json:"classpath"`
}

// Run locates the JDK and resolves the coordinates via the selected tier.
func Run(ctx context.Context, d Deps, javaVersion string, specs []pkg.Spec, tier string) (Result, error) {
	javaBin, err := java.Locate(ctx, d.FS, d.Root, javaVersion, d.Arch, d.Libc)
	if err != nil {
		return Result{}, err
	}
	if resolveTier(tier) == pkg.TierRelay {
		return Result{}, fmt.Errorf("java dependency relay (Tier 1) is not yet implemented; use --tier mirror with a sandbox-reachable Maven repository")
	}
	m := New(Config{
		Runner:        d.Runner,
		FS:            d.FS,
		JavaBin:       javaBin,
		ToolsDir:      path.Join(d.Root, "tools"),
		CoursierCache: path.Join(d.Root, "runtimes", "coursier-cache"),
		MirrorURL:     d.MirrorURL,
		CacheDir:      d.CacheDir,
		HTTPClient:    d.HTTPClient,
	})
	out, cp, err := m.Resolve(ctx, specs)
	if err != nil {
		return Result{}, err
	}
	return Result{Installed: out.Installed, Failed: out.Failed, Classpath: cp}, nil
}

// resolveTier maps the requested tier to a concrete one. "relay" is explicit-only
// for now (not yet implemented); "auto"/""/"mirror" resolve in the sandbox (Tier 0).
func resolveTier(tier string) string {
	if tier == pkg.TierRelay {
		return pkg.TierRelay
	}
	return pkg.TierMirror
}

type installParams struct {
	JavaVersion string `json:"java_version"`
	Packages    []struct {
		Name    string `json:"name"` // "group:artifact"
		Version string `json:"version"`
	} `json:"packages"`
}

// Handler adapts maven.Run into the maven.install protocol handler.
func Handler(d Deps) protocol.Handler {
	return func(ctx context.Context, req protocol.Request) protocol.Response {
		var p installParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return protocol.Errf(req.ID, "malformed_params", "parse params: %v", err)
		}
		if p.JavaVersion == "" {
			return protocol.Errf(req.ID, "missing_java_version", "maven.install requires params.java_version")
		}
		if len(p.Packages) == 0 {
			return protocol.Errf(req.ID, "no_packages", "maven.install requires at least one coordinate")
		}
		specs := make([]pkg.Spec, len(p.Packages))
		for i, pp := range p.Packages {
			specs[i] = pkg.Spec{Name: pp.Name, Version: pp.Version}
		}
		out, err := Run(ctx, d, p.JavaVersion, specs, "auto")
		if err != nil {
			return protocol.Errf(req.ID, "install_failed", "%v", err)
		}
		return protocol.OK(req.ID, out)
	}
}
