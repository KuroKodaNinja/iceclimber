//go:build scenario

package javaapp

import (
	"context"
	_ "embed"
	"encoding/json"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/KuroKodaNinja/iceclimber/test/scenarios/harness"
)

//go:embed mvnapp/pom.xml
var pomXML []byte

//go:embed mvnapp/src/main/java/com/example/App.java
var mvnAppJava []byte

// TestJavaMavenBuild runs the Maven BUILD TOOL inside the sandbox, air-gapped: a real
// pom.xml project (with a Gson dependency) is built by `mvn -o package` in the sandbox,
// after the controller primes an offline .m2 repo (resolves + downloads every dep +
// plugin) and Popo relays the Maven tool + that repo in. The built jar is then run — using
// Gson from the relayed repo — against the xkcd comic fetched through Popo. Skips without
// Maven + a JDK on the controller (the prime engine), mirroring the conda relay's
// controller-conda dependency.
func TestJavaMavenBuild(t *testing.T) {
	if !controllerHasMaven() {
		t.Skip("maven.build needs Maven + a JDK on the controller (the offline-repo prime engine)")
	}
	sb := harness.Require(t)
	root := sb.NewRoot(t)
	// This exercises the RELAY maven build (controller primes an offline .m2, sandbox builds
	// `mvn -o`); the harness pins egress_mode: relay for all scenarios. The proxy-mode online
	// build is covered by TestProxyMavenBuild (it needs a live serve for the tunnel).
	cfg := sb.WriteConfig(t, root, `network:
  allowed_domains:
    - pattern: "xkcd.com"
      reachable_from: sandbox`)

	sb.Run(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	fs := sb.DialFS(t, "sftp")
	ctx := context.Background()
	proj := path.Join(root, "xkcdtool")
	if err := fs.Mkdir(ctx, path.Join(proj, "src", "main", "java", "com", "example")); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	// 1. Fetch the comic through Popo and stage it as the app's input.
	body := sb.Fetch(t, fs, cfg, root, "https://xkcd.com/info.0.json")
	var comic map[string]any
	if err := json.Unmarshal(body, &comic); err != nil {
		t.Fatalf("parse comic JSON: %v\nbody: %s", err, body)
	}
	if _, ok := comic["num"].(float64); !ok {
		t.Fatalf("fetched JSON missing numeric num: %v", comic)
	}
	if err := fs.WriteFile(ctx, path.Join(proj, "comic.json"), body); err != nil {
		t.Fatalf("write comic.json: %v", err)
	}

	// 2. Provision a sandbox JDK and deploy the real Maven project (pom.xml + source).
	sb.Run(t, "install", "java", "21", "--config", cfg, "--transport", "sftp")
	if err := fs.WriteFile(ctx, path.Join(proj, "pom.xml"), pomXML); err != nil {
		t.Fatalf("write pom.xml: %v", err)
	}
	if err := fs.WriteFile(ctx, path.Join(proj, "src", "main", "java", "com", "example", "App.java"), mvnAppJava); err != nil {
		t.Fatalf("write App.java: %v", err)
	}

	// 3. Build it with mvn in the sandbox, air-gapped (controller primes, sandbox builds
	//    offline).
	buildOut := string(sb.Run(t, "maven", "build", "--project", proj, "--java", "21",
		"--config", cfg, "--transport", "sftp"))
	if !strings.Contains(buildOut, "target/xkcdtool.jar") {
		t.Fatalf("maven build did not produce the jar:\n%s", buildOut)
	}

	// 4. Run the built jar with Gson from the relayed offline repo.
	out := sb.Sh(t, "set -e\n"+
		"GSON=$(ls "+shq(path.Join(root, "runtimes", "maven-repo"))+"/com/google/code/gson/gson/*/gson-*.jar)\n"+
		"JAVA=$(ls "+shq(path.Join(root, "runtimes", "java"))+"/*/bin/java)\n"+
		`"$JAVA" -cp `+shq(path.Join(proj, "target", "xkcdtool.jar"))+`:"$GSON" com.example.App `+shq(path.Join(proj, "comic.json")))

	// 5. Assert the built program ran, Gson loaded (from the offline repo), and it
	//    processed the fetched data. Java String.length() is UTF-16 units.
	num := strconv.Itoa(int(comic["num"].(float64)))
	title, _ := comic["title"].(string)
	titleLen := strconv.Itoa(len(utf16.Encode([]rune(title))))
	for _, want := range []string{"MAVEN_BUILD_OK", num, title, "title length: " + titleLen} {
		if !strings.Contains(out, want) {
			t.Errorf("app output is missing %q:\n%s", want, out)
		}
	}
}

// controllerHasMaven reports whether the controller has both Maven and a working JDK —
// the engine that primes the offline .m2 repo for maven.build.
func controllerHasMaven() bool {
	if _, err := exec.LookPath("mvn"); err != nil || exec.Command("mvn", "-v").Run() != nil {
		return false
	}
	if _, err := exec.LookPath("java"); err != nil || exec.Command("java", "-version").Run() != nil {
		return false
	}
	return true
}
