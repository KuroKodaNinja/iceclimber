//go:build functional

package functional

import (
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestMavenResolve installs a JDK, resolves a real Maven dependency (Guava and its
// transitive deps) into a classpath via Coursier, and then compiles+runs a program
// that actually uses the dependency — the JVM analogue of the pip/npm + scenario
// suites (resolve → build → run, all on the live Alpine/musl VM).
func TestMavenResolve(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-maven-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	// JDK first (the resolver runs on it).
	jout := string(runIceclimber(t, "install", "java", "21", "--config", cfg, "--transport", "sftp"))
	javaBin := afterAt(jout)
	if !strings.HasSuffix(javaBin, "/bin/java") {
		t.Fatalf("could not parse java path from %q", jout)
	}

	// Resolve Guava + its transitive deps into a classpath (Coursier; heavy first run).
	mout := string(runIceclimber(t, "install", "maven", "com.google.guava:guava:33.0.0-jre",
		"--java", "21", "--config", cfg, "--transport", "sftp"))
	if !strings.Contains(mout, "resolved com.google.guava:guava") {
		t.Fatalf("maven resolve output = %q", mout)
	}
	cp := classpathLine(mout)
	if !strings.Contains(cp, "guava") {
		t.Fatalf("classpath missing guava jar: %q", cp)
	}

	// Build + run a program that uses Guava (proves the classpath actually works).
	javac := strings.TrimSuffix(javaBin, "/java") + "/javac"
	prog := `import com.google.common.base.Joiner;` +
		` public class Use { public static void main(String[] a) {` +
		` System.out.println("guava-says " + Joiner.on('-').join("a", "b", "c")); } }`
	script := "set -e\n" +
		"cat > /tmp/Use.java <<'EOF'\n" + prog + "\nEOF\n" +
		"mkdir -p /tmp/mvnout\n" +
		remoteQuote(javac) + " -cp " + remoteQuote(cp) + " -d /tmp/mvnout /tmp/Use.java\n" +
		remoteQuote(javaBin) + " -cp " + remoteQuote(cp+":/tmp/mvnout") + " Use"
	out := limaSh(t, script)
	if !strings.Contains(out, "guava-says a-b-c") {
		t.Errorf("running the Guava program = %q, want guava-says a-b-c", strings.TrimSpace(out))
	}
}

// afterAt returns the path after " at " in an install command's output line
// ("installed java 21.0.11+10 at /…/bin/java").
func afterAt(s string) string {
	if i := strings.LastIndex(s, " at "); i >= 0 {
		return strings.TrimSpace(s[i+4:])
	}
	return ""
}

// classpathLine extracts the "classpath: …" line from `install maven` output.
func classpathLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(ln), "classpath:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
