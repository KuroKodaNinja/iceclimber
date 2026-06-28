// Package maven resolves JVM (Maven-coordinate) dependencies for the Java runtime,
// the third language's package manager (plan §5, decision #22). It uses Coursier:
// the 25 kB JAR launcher is relayed into the sandbox once (platform-independent) and
// run via the installed JDK to resolve a set of coordinates and their transitive
// dependencies into a classpath. Tier 0 runs in the sandbox against Maven Central
// (or a configured mirror); the Tier 1 relay (controller resolves, Popo relays the
// JARs in) is the next increment — and is trivially correct because JVM bytecode is
// platform-independent (no cross-platform-wheel problem).
package maven

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
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
	JavaBin       string // <jdk>/bin/java
	ToolsDir      string // sandbox dir for the relayed coursier launcher
	CoursierCache string // sandbox COURSIER_CACHE (artifacts live here, classpath references them)
	MirrorURL     string // optional Maven repository (Tier 0); empty = Maven Central
	CacheDir      string // controller cache for the launcher download
	HTTPClient    *http.Client
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
// a classpath, downloading the JARs into the sandbox's Coursier cache. Coursier
// resolves the whole set together (one unified resolution), so the request succeeds
// or fails as a unit.
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
		var out pkg.Outcome
		msg := lastLines(res.Stderr, 4)
		for _, s := range specs {
			out.Failed = append(out.Failed, pkg.Failure{Name: s.Name, Version: s.Version, Error: msg})
		}
		return out, "", nil
	}

	classpath := lastNonEmpty(string(res.Stdout))
	var out pkg.Outcome
	for _, s := range specs {
		out.Installed = append(out.Installed, pkg.Installed{Name: s.Name, Version: s.Version, Tier: pkg.TierMirror})
	}
	return out, classpath, nil
}

// ensureCoursier makes sure the Coursier launcher is present in the sandbox,
// downloading it on the controller (cached) and relaying it in once.
func (m *Manager) ensureCoursier(ctx context.Context) (string, error) {
	dst := path.Join(m.cfg.ToolsDir, "coursier.jar")
	if ok, err := m.exists(ctx, dst); err != nil {
		return "", err
	} else if ok {
		return dst, nil
	}
	data, err := m.fetchLauncher(ctx)
	if err != nil {
		return "", fmt.Errorf("fetch coursier launcher: %w", err)
	}
	_ = m.cfg.FS.Mkdir(ctx, m.cfg.ToolsDir) // best-effort; WriteFile surfaces real errors
	if err := m.cfg.FS.WriteFile(ctx, dst, data); err != nil {
		return "", fmt.Errorf("relay coursier launcher: %w", err)
	}
	return dst, nil
}

func (m *Manager) exists(ctx context.Context, p string) (bool, error) {
	names, err := m.cfg.FS.List(ctx, path.Dir(p))
	if err != nil {
		return false, nil // dir missing ⇒ not present (List errors are treated as absent here)
	}
	base := path.Base(p)
	for _, n := range names {
		if n == base {
			return true, nil
		}
	}
	return false, nil
}

// fetchLauncher downloads the Coursier launcher to the controller cache (keyed by
// name) and reuses a cached copy, returning its bytes.
func (m *Manager) fetchLauncher(ctx context.Context) ([]byte, error) {
	cache := m.cfg.CacheDir
	if cache == "" {
		cache = filepath.Join(os.TempDir(), "iceclimber-cache")
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return nil, err
	}
	dst := filepath.Join(cache, "coursier.jar")
	if data, err := os.ReadFile(dst); err == nil && len(data) > 0 {
		return data, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coursierLauncherURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", coursierLauncherURL, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	// Write atomically into the cache (best-effort; a failed cache write is non-fatal).
	tmp := dst + ".tmp." + hex.EncodeToString(sha(data)[:6])
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, dst)
	}
	return data, nil
}

func sha(b []byte) []byte { h := sha256.Sum256(b); return h[:] }

// ref renders a spec as a Coursier/Maven coordinate. Name is "group:artifact".
func ref(s pkg.Spec) string {
	if s.Version == "" {
		return s.Name
	}
	return s.Name + ":" + s.Version
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
