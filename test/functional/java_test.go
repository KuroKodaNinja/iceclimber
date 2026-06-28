//go:build functional

package functional

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestJavaInstall installs a Temurin JDK into the real Alpine/musl VM, proves the
// java.install verb routes to it, and then compiles+runs a Java program by absolute
// path — the JDK analogue of the node/python install tests plus an app build.
//
// The JDK is large (~200 MB extracted, thousands of files), so the heavy install
// goes through the `install java` command (15-minute budget); the verb then sees it
// AlreadyInstalled and returns fast, within serve --once's 2-minute cap.
func TestJavaInstall(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-java-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root)

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	// Heavy install via the CLI (long timeout). javac ships in the JDK.
	out := runIceclimber(t, "install", "java", "21", "--config", cfg, "--transport", "sftp")
	if !strings.Contains(string(out), "java 21.") {
		t.Fatalf("install java 21 output = %q, want a 21.x version", string(out))
	}

	// The java.install verb routes to the same installer and now sees it present.
	fs, cleanup := dialFS(t, sb, "sftp")
	defer cleanup()
	ctx := context.Background()
	tree := protocol.Tree{Root: root}

	id := protocol.NewID()
	name := protocol.RequestName(id)
	data, _ := json.Marshal(protocol.Request{
		SchemaVersion: protocol.SchemaVersion, ID: id, Type: "java.install",
		CreatedAt: time.Now().UTC(), Params: json.RawMessage(`{"version":"21"}`),
	})
	if err := protocol.Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
		t.Fatalf("deliver java.install: %v", err)
	}
	runIceclimber(t, "serve", "--once", "--config", cfg, "--transport", "sftp")

	resp, err := protocol.ReadResponse(ctx, fs, tree, name)
	if err != nil {
		t.Fatalf("read java.install response: %v", err)
	}
	if resp.Status != protocol.StatusOK {
		t.Fatalf("java.install status = %q, error = %+v", resp.Status, resp.Error)
	}
	var r struct {
		Version          string `json:"version"`
		Path             string `json:"path"`
		AlreadyInstalled bool   `json:"already_installed"`
	}
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !strings.HasPrefix(r.Version, "21") || !strings.HasSuffix(r.Path, "/bin/java") || !r.AlreadyInstalled {
		t.Fatalf("result = %+v, want version 21.x, a bin/java path, already_installed", r)
	}

	// java -version (prints to stderr) runs by absolute path (the §2 contract).
	if v := limaSh(t, remoteQuote(r.Path)+" -version 2>&1"); !strings.Contains(v, "21") {
		t.Errorf("java -version = %q, want a 21.x runtime", strings.TrimSpace(v))
	}

	// javac is present (this is a JDK, not just a JRE).
	javac := strings.TrimSuffix(r.Path, "/java") + "/javac"
	if v := limaSh(t, remoteQuote(javac)+" -version 2>&1"); !strings.Contains(v, "javac 21") {
		t.Errorf("javac -version = %q, want javac 21.x", strings.TrimSpace(v))
	}

	// Build + run a real program (single-file source launch, JEP 330).
	prog := "public class Hello { public static void main(String[] a) {" +
		" System.out.println(\"hello-from-java \" + System.getProperty(\"java.version\")); } }"
	script := "cat > /tmp/Hello.java <<'EOF'\n" + prog + "\nEOF\n" + remoteQuote(r.Path) + " /tmp/Hello.java"
	run := limaSh(t, script)
	if !strings.Contains(run, "hello-from-java 21") {
		t.Errorf("running Hello.java = %q, want hello-from-java 21.x", strings.TrimSpace(run))
	}
}
