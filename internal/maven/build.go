package maven

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/java"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// DefaultMavenToolVersion is the Apache Maven distribution relayed into the sandbox to
// run the build. Pinned; the archive host keeps every release.
const DefaultMavenToolVersion = "3.9.9"

func mavenToolURL(v string) string {
	return fmt.Sprintf("https://archive.apache.org/dist/maven/maven-3/%s/binaries/apache-maven-%s-bin.tar.gz", v, v)
}

// parseMvnVersion extracts "3.9.12" from `mvn -v`'s first line ("Apache Maven 3.9.12
// (…)"). Returns "" if the shape is unexpected.
func parseMvnVersion(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "Apache Maven "); ok {
			if i := strings.IndexByte(after, ' '); i > 0 {
				return after[:i]
			}
			return strings.TrimSpace(after)
		}
	}
	return ""
}

// BuildDeps are what a maven.build needs: where to run, the controller's maven + java
// (to prime the offline repo), and the HTTP client for the Maven-tool download.
type BuildDeps struct {
	FS             remotefs.FS
	Runner         remote.Runner
	Root           string
	Arch           string
	Libc           string
	ControllerMvn  string // operator's mvn on the controller (default "mvn")
	ControllerJava string // operator's java on the controller (default "java")
	EgressProxy    bool   // egress_mode: proxy → build ONLINE through the MITM proxy (no controller prime)
	Progress       progress.Func
}

// BuildResult is the maven.build response body: the built artifacts (sandbox paths) and
// the tier used.
type BuildResult struct {
	Artifacts []string `json:"artifacts"`
	Tier      string   `json:"tier"`
}

// Build runs a Maven build of a sandbox project entirely air-gapped (the conda/pip relay
// analogue for the Maven build tool). The controller primes a local Maven repository by
// actually building the project, then Popo relays the Maven tool + that primed repo into
// the sandbox, which re-runs the build OFFLINE (`mvn -o package`) with no network. javac
// runs in the sandbox against its own JDK; nothing leaves the sandbox's disk.
//
// projectDir is a sandbox directory holding a pom.xml (and src/). javaVersion selects the
// already-installed sandbox JDK (JAVA_HOME); goals default to ["package"].
func Build(ctx context.Context, d BuildDeps, projectDir, javaVersion, mavenVersion string, goals []string) (BuildResult, error) {
	if len(goals) == 0 {
		goals = []string{"package"}
	}
	if d.EgressProxy {
		// Proxy mode: the sandbox reaches the real registry through Popo's MITM proxy, so
		// the native mvn resolves + downloads + runs plugins ONLINE — no controller prime,
		// no offline repo relay. This is the proxy's payoff: full Maven behavior (lifecycle
		// plugins, transitive + plugin resolution, download-during-build) with no bespoke Go.
		return d.buildOnlineProxy(ctx, projectDir, javaVersion, mavenVersion, goals)
	}
	cmvn := firstNonEmptyStr(d.ControllerMvn, "mvn")
	vout, err := exec.CommandContext(ctx, cmvn, "-v").CombinedOutput()
	if err != nil {
		return BuildResult{}, fmt.Errorf("maven.build needs Maven + a JDK on the controller (set controller_mvn): %v: %s", err, lastLines(vout, 3))
	}
	// Relay the controller's EXACT Maven version so the sandbox build sees the same
	// super-pom plugin defaults the controller primed — no version drift, no missing
	// plugin offline. Fall back to the pinned default if the version can't be parsed.
	if mavenVersion == "" {
		if v := parseMvnVersion(string(vout)); v != "" {
			mavenVersion = v
		} else {
			mavenVersion = DefaultMavenToolVersion
		}
	}

	// The sandbox JDK the offline build compiles against (JAVA_HOME).
	javaBin, err := java.Locate(ctx, d.FS, d.Root, javaVersion, d.Arch, d.Libc)
	if err != nil {
		return BuildResult{}, err
	}
	javaHome := path.Dir(path.Dir(javaBin)) // <jdk>/bin/java → <jdk>

	// 1. Read ONLY the project's pom.xml from the sandbox (go-offline resolves from the
	//    POM alone — no sources needed). Reading a single known path avoids walking
	//    sandbox-controlled directory entries (a path-traversal vector) entirely.
	d.Progress.Phase("staging pom")
	stage, err := os.MkdirTemp("", "iceclimber-mvnbuild-")
	if err != nil {
		return BuildResult{}, err
	}
	defer os.RemoveAll(stage)
	pomBytes, err := d.FS.ReadFile(ctx, path.Join(projectDir, "pom.xml"))
	if err != nil {
		return BuildResult{}, fmt.Errorf("read %s/pom.xml (create the Maven project first): %w", projectDir, err)
	}
	stagePom := filepath.Join(stage, "pom.xml")
	if err := os.WriteFile(stagePom, pomBytes, 0o644); err != nil {
		return BuildResult{}, err
	}

	// 2. Prime the offline repo by RESOLVING (not building) — `dependency:go-offline`
	//    downloads every dependency + plugin the build needs without executing the
	//    project's lifecycle plugins, so an untrusted pom.xml cannot run code on the
	//    controller (the pip-download / conda-dry-run philosophy, applied to Maven).
	d.Progress.Phase("priming repo (controller)")
	stageRepo := filepath.Join(stage, "m2repo")
	primeArgs := []string{"-B", "-ntp", "-Dmaven.repo.local=" + stageRepo, "-f", stagePom, "dependency:go-offline"}
	if out, err := exec.CommandContext(ctx, cmvn, primeArgs...).CombinedOutput(); err != nil {
		return BuildResult{}, fmt.Errorf("controller repo prime (dependency:go-offline) failed — a pinned plugin/dep may be unresolvable: %s", lastLines(out, 8))
	}

	// 3. Relay the Maven tool (once) and the primed repo into the sandbox.
	mvnBin, err := d.ensureMavenTool(ctx, mavenVersion)
	if err != nil {
		return BuildResult{}, err
	}
	d.Progress.Phase("relaying repo")
	sandboxRepo := path.Join(d.Root, "runtimes", "maven-repo")
	if err := pushTree(ctx, d.FS, d.Progress, stageRepo, sandboxRepo); err != nil {
		return BuildResult{}, fmt.Errorf("relay maven repo: %w", err)
	}

	// 4. Sandbox re-runs the build OFFLINE from the relayed repo.
	d.Progress.Phase("building (offline)")
	sandboxArgs := []string{
		"JAVA_HOME=" + remote.ShellQuote(javaHome),
		remote.ShellQuote(mvnBin), "-o", "-B", "-ntp",
		"-Dmaven.repo.local=" + remote.ShellQuote(sandboxRepo),
		"-f", remote.ShellQuote(path.Join(projectDir, "pom.xml")),
	}
	for _, g := range goals {
		sandboxArgs = append(sandboxArgs, remote.ShellQuote(g))
	}
	res, err := d.Runner.Run(ctx, strings.Join(sandboxArgs, " "), nil)
	if err != nil {
		return BuildResult{}, fmt.Errorf("run offline mvn: %w", err)
	}
	if res.ExitCode != 0 {
		return BuildResult{}, fmt.Errorf("offline mvn build failed: %s", lastLines(res.Stderr, 8))
	}

	// 5. Collect the built artifacts (target/*.jar).
	return BuildResult{Artifacts: d.collectArtifacts(ctx, projectDir), Tier: "relay"}, nil
}

// buildOnlineProxy runs the build in egress-proxy mode: push the native Maven tool, build a
// JVM truststore for the egress CA (the JVM ignores the OpenSSL/Node CA env vars), then run
// mvn ONLINE against the real registry through the proxy — routed via the bootstrap-written
// maven-settings.xml <proxies> block (Maven honors settings, not the JVM proxy props). No
// controller-side prime, no offline repo relay: the proxy IS the network mediation.
func (d BuildDeps) buildOnlineProxy(ctx context.Context, projectDir, javaVersion, mavenVersion string, goals []string) (BuildResult, error) {
	javaBin, err := java.Locate(ctx, d.FS, d.Root, javaVersion, d.Arch, d.Libc)
	if err != nil {
		return BuildResult{}, err
	}
	javaHome := path.Dir(path.Dir(javaBin)) // <jdk>/bin/java → <jdk>
	if mavenVersion == "" {
		mavenVersion = DefaultMavenToolVersion
	}
	mvnBin, err := d.ensureMavenTool(ctx, mavenVersion)
	if err != nil {
		return BuildResult{}, err
	}

	// Trust: import the egress CA (written to certs/egress-ca.pem at bootstrap) into a JVM
	// truststore so mvn validates the proxy's minted leaves.
	d.Progress.Phase("egress truststore")
	caPath := path.Join(d.Root, "certs", "egress-ca.pem")
	storePath := path.Join(d.Root, "certs", "java-truststore.p12")
	settings := path.Join(d.Root, "maven-settings.xml")
	if err := java.EnsureEgressTrustStore(ctx, d.Runner, javaBin, caPath, storePath); err != nil {
		return BuildResult{}, err
	}

	d.Progress.Phase("building (online via proxy)")
	mavenOpts := fmt.Sprintf("-Djavax.net.ssl.trustStore=%s -Djavax.net.ssl.trustStorePassword=%s", storePath, java.EgressTrustStorePass)
	args := []string{
		"JAVA_HOME=" + remote.ShellQuote(javaHome),
		"MAVEN_OPTS=" + remote.ShellQuote(mavenOpts),
		remote.ShellQuote(mvnBin), "-B", "-ntp",
		"-s", remote.ShellQuote(settings),
		"-f", remote.ShellQuote(path.Join(projectDir, "pom.xml")),
	}
	for _, g := range goals {
		args = append(args, remote.ShellQuote(g))
	}
	res, err := d.Runner.Run(ctx, strings.Join(args, " "), nil)
	if err != nil {
		return BuildResult{}, fmt.Errorf("run online mvn: %w", err)
	}
	if res.ExitCode != 0 {
		// mvn logs to stdout (with -B), so combine both streams for the tail.
		combined := string(res.Stdout) + string(res.Stderr)
		if looksLikeProxyDown(combined) {
			return BuildResult{}, fmt.Errorf("online mvn build failed — the egress proxy is not reachable: proxy-mode `maven build` needs an active `iceclimber serve` (the proxy only listens during a serve session), or use egress_mode: relay for a one-shot build:\n%s", lastLines([]byte(combined), 8))
		}
		return BuildResult{}, fmt.Errorf("online mvn build failed: %s", lastLines([]byte(combined), 8))
	}
	return BuildResult{Artifacts: d.collectArtifacts(ctx, projectDir), Tier: "proxy"}, nil
}

// looksLikeProxyDown reports whether mvn's output indicates it couldn't reach the egress
// proxy on the sandbox loopback — the tell that proxy-mode `maven build` was run without an
// active serve holding the reverse tunnel up.
func looksLikeProxyDown(out string) bool {
	for _, m := range []string{"Connection refused", "ConnectException", "127.0.0.1:", "Connection to 127.0.0.1"} {
		if strings.Contains(out, m) {
			return true
		}
	}
	return false
}

// collectArtifacts lists the build's target/*.jar outputs (sandbox paths), sorted. Runs only
// after a successful build, so a List error is treated as "no jars produced" (best-effort
// reporting — the build itself already succeeded); it is intentionally not surfaced.
func (d BuildDeps) collectArtifacts(ctx context.Context, projectDir string) []string {
	target := path.Join(projectDir, "target")
	var artifacts []string
	if names, lerr := d.FS.List(ctx, target); lerr == nil {
		for _, n := range names {
			if strings.HasSuffix(n, ".jar") {
				artifacts = append(artifacts, path.Join(target, n))
			}
		}
	}
	sort.Strings(artifacts)
	return artifacts
}

// ensureMavenTool relays the Apache Maven distribution into the sandbox once and returns
// the absolute path to its bin/mvn. Idempotent (reuses an existing install).
func (d BuildDeps) ensureMavenTool(ctx context.Context, version string) (string, error) {
	toolDir := path.Join(d.Root, "runtimes", "maven", version)
	mvnBin := path.Join(toolDir, "bin", "mvn")
	if names, err := d.FS.List(ctx, path.Join(toolDir, "bin")); err == nil {
		for _, n := range names {
			if n == "mvn" {
				return mvnBin, nil
			}
		}
	}
	d.Progress.Phase("relaying maven tool")
	url := mavenToolURL(version)
	tmp, err := os.CreateTemp("", "apache-maven-*.tar.gz")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := downloadTo(ctx, url, tmp); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("download Maven %s: %w", version, err)
	}
	// Verify the tarball against Apache's published SHA-512 before extracting it into the
	// sandbox (defense-in-depth over TLS; the tool is executed in the sandbox).
	if err := verifyFileSHA512(ctx, tmp, url); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("verify Maven %s: %w", version, err)
	}
	if _, err := tmp.Seek(0, 0); err != nil {
		return "", err
	}
	if err := d.FS.Mkdir(ctx, toolDir); err != nil {
		return "", err
	}
	// PushTarGz strips the archive's leading apache-maven-<ver>/ dir, so bin/mvn lands
	// directly under toolDir.
	if err := remotefs.PushTarGz(ctx, d.FS, tmp, toolDir); err != nil {
		return "", fmt.Errorf("extract Maven tool: %w", err)
	}
	_ = tmp.Close()
	return mvnBin, nil
}

// verifyFileSHA512 rewinds f, computes its SHA-512, and compares it to Apache's published
// <url>.sha512. f is left rewound for the caller.
func verifyFileSHA512(ctx context.Context, f *os.File, url string) error {
	want, err := expectedSHA512(ctx, url)
	if err != nil {
		return fmt.Errorf("fetch published sha512: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("sha512 mismatch: published %s, downloaded %s", want, got)
	}
	return nil
}
