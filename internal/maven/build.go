package maven

import (
	"context"
	"fmt"
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
	cmvn := firstNonEmptyStr(d.ControllerMvn, "mvn")
	cjava := firstNonEmptyStr(d.ControllerJava, "java")
	vout, err := exec.CommandContext(ctx, cmvn, "-v").CombinedOutput()
	if err != nil {
		return BuildResult{}, fmt.Errorf("maven.build needs Maven on the controller (set controller_mvn): %v: %s", err, lastLines(vout, 3))
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

	// 1. Pull the project (pom.xml + sources) from the sandbox to a controller stage.
	d.Progress.Phase("staging project")
	stage, err := os.MkdirTemp("", "iceclimber-mvnbuild-")
	if err != nil {
		return BuildResult{}, err
	}
	defer os.RemoveAll(stage)
	stageProj := filepath.Join(stage, "project")
	if err := pullTree(ctx, d.FS, projectDir, stageProj); err != nil {
		return BuildResult{}, fmt.Errorf("stage project from sandbox: %w", err)
	}
	if _, err := os.Stat(filepath.Join(stageProj, "pom.xml")); err != nil {
		return BuildResult{}, fmt.Errorf("no pom.xml in %s (create the Maven project first)", projectDir)
	}

	// 2. Controller build primes the offline repo (downloads every dep + plugin the
	//    build needs, and confirms it builds). JAVA_HOME left to the controller's own.
	d.Progress.Phase("priming repo (controller)")
	stageRepo := filepath.Join(stage, "m2repo")
	primeArgs := append([]string{"-B", "-ntp", "-Dmaven.repo.local=" + stageRepo, "-f", filepath.Join(stageProj, "pom.xml")}, goals...)
	if out, err := exec.CommandContext(ctx, cmvn, primeArgs...).CombinedOutput(); err != nil {
		return BuildResult{}, fmt.Errorf("controller prime build failed: %s", lastLines(out, 8))
	}
	_ = cjava // controller mvn finds java itself; cjava documents the dependency

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
	return BuildResult{Artifacts: artifacts, Tier: "relay"}, nil
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
