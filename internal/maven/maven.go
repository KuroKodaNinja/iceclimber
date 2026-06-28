// Package maven resolves JVM (Maven-coordinate) dependencies for the Java runtime,
// the third language's package manager (plan §5, decision #22). It uses Coursier:
// the 25 kB JAR launcher is platform-independent, so the same launcher runs on the
// sandbox's JDK (Tier 0) or the controller's java (Tier 1). Resolution turns a set
// of coordinates + their transitive deps into a classpath.
//
//   - Tier 0 (mirror): the launcher is relayed into the sandbox and run there
//     against Maven Central (or a configured mirror); the classpath references the
//     sandbox's Coursier cache.
//   - Tier 1 (relay): the controller's java resolves + downloads the JARs on its own
//     network, Popo relays the JARs into the sandbox, and the classpath references
//     the relayed copies. Trivially correct because JVM bytecode is
//     platform-independent (no cross-platform-wheel problem; JNI native libs would
//     be the rare "Tier 2", deferred).
package maven

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// coursierLauncherURL is the stable 25 kB JAR-based launcher (run with `java -jar`).
const coursierLauncherURL = "https://github.com/coursier/launchers/raw/master/coursier"

// Config holds the resolver's dependencies.
type Config struct {
	Runner        remote.Runner
	FS            remotefs.FS
	JavaBin       string // <jdk>/bin/java (sandbox, for Tier 0)
	ToolsDir      string // sandbox dir for the relayed coursier launcher (Tier 0)
	CoursierCache string // sandbox COURSIER_CACHE (Tier 0 artifacts live here)
	RelayDir      string // sandbox dir the Tier 1 relayed JARs land in
	MirrorURL     string // Tier 0: sandbox-reachable Maven repository (empty = Central)
	// Tier 1 (relay) only:
	ControllerJava       string // operator's java on the controller (default "java")
	ControllerRepository string // Popo-reachable Maven repository (empty = Central)
	CacheDir             string // controller cache for the launcher download
	HTTPClient           *http.Client
}

// Manager resolves JVM dependencies for one Java runtime.
type Manager struct {
	cfg Config
}

// New builds a resolver.
func New(cfg Config) *Manager {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Manager{cfg: cfg}
}

// Resolve (Tier 0) resolves the coordinates and their transitive dependencies into
// a classpath in the sandbox, downloading the JARs into the sandbox's Coursier
// cache. Coursier resolves the whole set together, so the request succeeds/fails as
// a unit.
func (m *Manager) Resolve(ctx context.Context, specs []pkg.Spec) (pkg.Outcome, string, error) {
	launcher, err := m.ensureCoursier(ctx)
	if err != nil {
		return pkg.Outcome{}, "", err
	}

	args := []string{remote.ShellQuote(m.cfg.JavaBin), "-jar", remote.ShellQuote(launcher), "fetch", "--classpath"}
	if m.cfg.MirrorURL != "" {
		args = append(args, "-r", remote.ShellQuote(m.cfg.MirrorURL))
	}
	for _, s := range specs {
		args = append(args, remote.ShellQuote(ref(s)))
	}
	cmd := "COURSIER_CACHE=" + remote.ShellQuote(m.cfg.CoursierCache) + " " + strings.Join(args, " ")

	res, err := m.cfg.Runner.Run(ctx, cmd, nil)
	if err != nil {
		return pkg.Outcome{}, "", err
	}
	if res.ExitCode != 0 {
		return failed(specs, lastLines(res.Stderr, 4)), "", nil
	}
	return resolvedOutcome(specs, pkg.TierMirror), lastNonEmpty(string(res.Stdout)), nil
}

// RelayResolve (Tier 1) resolves + downloads the JARs with the controller's java on
// its own network, relays them into the sandbox, and returns a classpath pointing
// at the relayed copies. The sandbox needs no network and no Coursier.
func (m *Manager) RelayResolve(ctx context.Context, specs []pkg.Spec) (pkg.Outcome, string, error) {
	cjava := m.cfg.ControllerJava
	if cjava == "" {
		cjava = "java"
	}
	if out, err := exec.CommandContext(ctx, cjava, "-version").CombinedOutput(); err != nil {
		return pkg.Outcome{}, "", fmt.Errorf("Tier 1 relay needs java on the controller (set controller_java): %v: %s", err, strings.TrimSpace(string(out)))
	}
	launcher, err := m.controllerLauncher(ctx)
	if err != nil {
		return pkg.Outcome{}, "", err
	}

	stage, err := os.MkdirTemp("", "iceclimber-mvn-")
	if err != nil {
		return pkg.Outcome{}, "", err
	}
	defer os.RemoveAll(stage)

	// 1. Controller resolves + downloads into the staging cache, printing the
	//    local classpath.
	args := []string{"-jar", launcher, "fetch", "--classpath"}
	if m.cfg.ControllerRepository != "" {
		args = append(args, "-r", m.cfg.ControllerRepository)
	}
	for _, s := range specs {
		args = append(args, ref(s))
	}
	c := exec.CommandContext(ctx, cjava, args...)
	c.Env = append(os.Environ(), "COURSIER_CACHE="+stage)
	stdout, err := c.Output()
	if err != nil {
		msg := err.Error()
		if ee, ok := err.(*exec.ExitError); ok {
			msg = lastLines(ee.Stderr, 4)
		}
		return failed(specs, msg), "", nil
	}
	localCP := lastNonEmpty(string(stdout))
	if localCP == "" {
		return pkg.Outcome{}, "", fmt.Errorf("coursier returned an empty classpath")
	}

	// 2. Relay each resolved JAR into the sandbox; the classpath references the
	//    relayed copies (platform-independent, so this is always correct).
	_ = m.cfg.FS.Mkdir(ctx, m.cfg.RelayDir) // best-effort; WriteFile surfaces real errors
	var sandboxJars []string
	seen := map[string]bool{}
	for _, local := range strings.Split(localCP, string(filepath.ListSeparator)) {
		local = strings.TrimSpace(local)
		if local == "" {
			continue
		}
		base := filepath.Base(local)
		dst := path.Join(m.cfg.RelayDir, base)
		if !seen[base] {
			data, err := os.ReadFile(local)
			if err != nil {
				return pkg.Outcome{}, "", fmt.Errorf("read resolved jar %s: %w", local, err)
			}
			if err := m.cfg.FS.WriteFile(ctx, dst, data); err != nil {
				return pkg.Outcome{}, "", fmt.Errorf("relay jar %s: %w", base, err)
			}
			seen[base] = true
		}
		sandboxJars = append(sandboxJars, dst)
	}
	return resolvedOutcome(specs, pkg.TierRelay), strings.Join(sandboxJars, ":"), nil
}

// ensureCoursier makes sure the Coursier launcher is present in the sandbox (Tier
// 0), relaying it in once from the controller's cached copy.
func (m *Manager) ensureCoursier(ctx context.Context) (string, error) {
	dst := path.Join(m.cfg.ToolsDir, "coursier.jar")
	if ok, err := m.exists(ctx, dst); err != nil {
		return "", err
	} else if ok {
		return dst, nil
	}
	local, err := m.controllerLauncher(ctx)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(local)
	if err != nil {
		return "", err
	}
	_ = m.cfg.FS.Mkdir(ctx, m.cfg.ToolsDir)
	if err := m.cfg.FS.WriteFile(ctx, dst, data); err != nil {
		return "", fmt.Errorf("relay coursier launcher: %w", err)
	}
	return dst, nil
}

func (m *Manager) exists(ctx context.Context, p string) (bool, error) {
	names, err := m.cfg.FS.List(ctx, path.Dir(p))
	if err != nil {
		return false, nil // dir missing ⇒ not present
	}
	base := path.Base(p)
	for _, n := range names {
		if n == base {
			return true, nil
		}
	}
	return false, nil
}

// controllerLauncher ensures the Coursier launcher is cached on the controller and
// returns its local path (downloading it once).
func (m *Manager) controllerLauncher(ctx context.Context) (string, error) {
	cache := m.cfg.CacheDir
	if cache == "" {
		cache = filepath.Join(os.TempDir(), "iceclimber-cache")
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(cache, "coursier.jar")
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
		return dst, nil
	}
	data, err := m.download(ctx, coursierLauncherURL)
	if err != nil {
		return "", fmt.Errorf("fetch coursier launcher: %w", err)
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func (m *Manager) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// ref renders a spec as a Coursier/Maven coordinate. Name is "group:artifact".
func ref(s pkg.Spec) string {
	if s.Version == "" {
		return s.Name
	}
	return s.Name + ":" + s.Version
}

func resolvedOutcome(specs []pkg.Spec, tier string) pkg.Outcome {
	var out pkg.Outcome
	for _, s := range specs {
		out.Installed = append(out.Installed, pkg.Installed{Name: s.Name, Version: s.Version, Tier: tier})
	}
	return out
}

func failed(specs []pkg.Spec, msg string) pkg.Outcome {
	var out pkg.Outcome
	for _, s := range specs {
		out.Failed = append(out.Failed, pkg.Failure{Name: s.Name, Version: s.Version, Error: msg})
	}
	return out
}

// lastNonEmpty returns the last non-blank line of s, trimmed (Coursier prints the
// classpath to stdout; any incidental output precedes it).
func lastNonEmpty(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
