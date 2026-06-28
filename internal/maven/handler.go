package maven

import (
	"context"
	"encoding/json"
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
	FS                   remotefs.FS
	Runner               remote.Runner
	Root                 string
	Arch                 string
	Libc                 string
	MirrorURL            string // Tier-0 sandbox-reachable Maven repository; empty = Central
	ControllerJava       string // Tier-1: operator's java on the controller (default "java")
	ControllerRepository string // Tier-1: Popo-reachable Maven repository; empty = Central
	CacheDir             string
	HTTPClient           *http.Client
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
	m := New(Config{
		Runner:               d.Runner,
		FS:                   d.FS,
		JavaBin:              javaBin,
		ToolsDir:             path.Join(d.Root, "tools"),
		CoursierCache:        path.Join(d.Root, "runtimes", "coursier-cache"),
		RelayDir:             path.Join(d.Root, "runtimes", "maven-relay"),
		MirrorURL:            d.MirrorURL,
		ControllerJava:       d.ControllerJava,
		ControllerRepository: d.ControllerRepository,
		CacheDir:             d.CacheDir,
		HTTPClient:           d.HTTPClient,
	})
	result := func(o pkg.Outcome, cp string) Result {
		return Result{Installed: o.Installed, Failed: o.Failed, Classpath: cp}
	}

	if resolveTier(tier, d.MirrorURL) == pkg.TierRelay {
		o, cp, err := m.RelayResolve(ctx, specs)
		if err != nil {
			return Result{}, err
		}
		return result(o, cp), nil
	}
	o, cp, err := m.Resolve(ctx, specs)
	if err != nil {
		return Result{}, err
	}
	// Tier 0→1 auto-fallback (mirror pip #25 / npm): if auto-mirror resolved
	// nothing, try the relay (controller-side).
	if (tier == "" || tier == "auto") && len(o.Installed) == 0 && len(o.Failed) > 0 {
		if ro, rcp, rerr := m.RelayResolve(ctx, specs); rerr == nil && len(ro.Installed) > 0 {
			return result(ro, rcp), nil
		}
	}
	return result(o, cp), nil
}

// resolveTier maps the requested tier to a concrete one. "auto" picks relay when no
// sandbox-reachable Maven repository is configured (the air-gapped default), else
// the sandbox-side mirror; "mirror"/"relay" force the choice.
func resolveTier(tier, mirrorURL string) string {
	switch tier {
	case pkg.TierRelay:
		return pkg.TierRelay
	case pkg.TierMirror:
		return pkg.TierMirror
	default: // "auto" or ""
		if mirrorURL == "" {
			return pkg.TierRelay
		}
		return pkg.TierMirror
	}
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
