//go:build functional

package cli

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/java"
	"github.com/KuroKodaNinja/iceclimber/internal/maven"
	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

// TestMavenRelay validates Tier 1 end to end against the live sandbox: the
// controller's java resolves + downloads Guava (and its transitive deps), Popo
// relays the JARs into the sandbox, and a program is compiled+run against the
// relayed classpath — the air-gap path (the sandbox never reaches Maven Central).
//
// Needs a working controller JDK; point ICECLIMBER_CONTROLLER_JAVA at a bin/java
// (or have a real `java` on PATH). Skips cleanly otherwise — like the npm Tier 1
// relay test without controller npm.
func TestMavenRelay(t *testing.T) {
	cjava := os.Getenv("ICECLIMBER_CONTROLLER_JAVA")
	if cjava == "" {
		cjava = "java"
	}
	if out, err := exec.Command(cjava, "-version").CombinedOutput(); err != nil {
		t.Skipf("no working controller java (%v: %s); set ICECLIMBER_CONTROLLER_JAVA", err, strings.TrimSpace(string(out)))
	}

	sess := consoleSession(t)
	defer sess.Close()
	ctx := context.Background()
	if err := provision(ctx, sess); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if _, err := newJavaInstaller(sess, nil).Install(ctx, "21"); err != nil {
		t.Fatalf("install java: %v", err)
	}

	// Force the relay tier and resolve on the controller.
	d := mavenDeps(sess, nil)
	d.ControllerJava = cjava
	res, err := maven.Run(ctx, d, "21", []pkg.Spec{{Name: "com.google.guava:guava", Version: "33.0.0-jre"}}, "relay")
	if err != nil {
		t.Fatalf("maven relay: %v", err)
	}
	if len(res.Installed) != 1 || res.Installed[0].Tier != pkg.TierRelay {
		t.Fatalf("relay result = %+v, want one relay-tier install", res)
	}
	if !strings.Contains(res.Classpath, "maven-relay") || !strings.Contains(res.Classpath, "guava") {
		t.Fatalf("classpath should reference the relayed guava jar: %q", res.Classpath)
	}

	// Compile + run a program against the RELAYED classpath in the sandbox.
	javaBin, err := java.Locate(ctx, sess.fs, sess.tree.Root, "21", sess.fp.Arch, sess.fp.Libc.Family)
	if err != nil {
		t.Fatalf("locate java: %v", err)
	}
	javac := strings.TrimSuffix(javaBin, "/java") + "/javac"
	prog := `import com.google.common.base.Joiner;` +
		` class Use { public static void main(String[] a) {` +
		` System.out.println("relay-guava " + Joiner.on('-').join("x", "y", "z")); } }`
	cp := remote.ShellQuote(res.Classpath)
	cpRun := remote.ShellQuote(res.Classpath + ":/tmp/relayout")
	script := "set -e\n" +
		"cat > /tmp/RelayUse.java <<'EOF'\n" + prog + "\nEOF\n" +
		"mkdir -p /tmp/relayout\n" +
		remote.ShellQuote(javac) + " -cp " + cp + " -d /tmp/relayout /tmp/RelayUse.java\n" +
		remote.ShellQuote(javaBin) + " -cp " + cpRun + " Use"
	r, err := sess.runner.Run(ctx, script, nil)
	if err != nil || r.ExitCode != 0 {
		t.Fatalf("build+run against relayed classpath failed (exit %d, err %v): %s", r.ExitCode, err, strings.TrimSpace(string(r.Stderr)))
	}
	if !strings.Contains(string(r.Stdout), "relay-guava x-y-z") {
		t.Errorf("program output = %q, want relay-guava x-y-z", strings.TrimSpace(string(r.Stdout)))
	}
}
