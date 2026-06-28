//go:build scenario

// Package javaapp is a self-contained, full-stack application scenario: it fetches
// data through Popo, provisions a JDK and a Maven dependency (Gson) in the sandbox,
// compiles and runs a real Java program that uses the dependency to process the
// fetched data, and asserts its computed output. See README.md in this directory.
// Run with `make scenario`.
package javaapp

import (
	"context"
	_ "embed"
	"encoding/json"
	"path"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/KuroKodaNinja/iceclimber/test/scenarios/harness"
)

//go:embed app/App.java
var appJava []byte

// TestJavaApp exercises the whole Java stack end to end: web.fetch (through Popo)
// → java.install → maven.install (Gson, Tier 0 against Central) → compile + run a
// real program → assert it rendered the computed report.
func TestJavaApp(t *testing.T) {
	sb := harness.Require(t)
	root := sb.NewRoot(t)
	cfg := sb.WriteConfig(t, root, `network:
  allowed_domains:
    - pattern: "xkcd.com"
      reachable_from: sandbox`)

	sb.Run(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	fs := sb.DialFS(t, "sftp")
	ctx := context.Background()
	work := path.Join(root, "work")
	if err := fs.Mkdir(ctx, work); err != nil {
		t.Fatalf("mkdir work: %v", err)
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
	if err := fs.WriteFile(ctx, path.Join(work, "comic.json"), body); err != nil {
		t.Fatalf("write comic.json: %v", err)
	}

	// 2. Provision the JDK + the Gson dependency (Tier 0 against Maven Central).
	jout := string(sb.Run(t, "install", "java", "21", "--config", cfg, "--transport", "sftp"))
	javaBin := afterAt(jout)
	if !strings.HasSuffix(javaBin, "/bin/java") {
		t.Fatalf("could not parse java path from %q", jout)
	}
	mout := string(sb.Run(t, "install", "maven", "com.google.code.gson:gson:2.10.1",
		"--java", "21", "--tier", "mirror", "--config", cfg, "--transport", "sftp"))
	cp := classpathLine(mout)
	if !strings.Contains(cp, "gson") {
		t.Fatalf("classpath missing gson jar:\n%s", mout)
	}

	// 3. Deploy, compile, and run the application against the resolved classpath.
	if err := fs.WriteFile(ctx, path.Join(work, "App.java"), appJava); err != nil {
		t.Fatalf("write App.java: %v", err)
	}
	javac := strings.TrimSuffix(javaBin, "/java") + "/javac"
	out := sb.Sh(t, "set -e\n"+
		shq(javac)+" -cp "+shq(cp)+" -d "+shq(work)+" "+shq(path.Join(work, "App.java"))+"\n"+
		shq(javaBin)+" -cp "+shq(cp+":"+work)+" App "+shq(path.Join(work, "comic.json")))

	// 4. Assert the report carries the computed values (program ran, Gson loaded,
	//    it processed the fetched data). Java String.length() is UTF-16 units.
	num := strconv.Itoa(int(comic["num"].(float64)))
	title, _ := comic["title"].(string)
	titleLen := strconv.Itoa(len(utf16.Encode([]rune(title))))
	for _, want := range []string{num, title, titleLen} {
		if !strings.Contains(out, want) {
			t.Errorf("app output is missing %q:\n%s", want, out)
		}
	}
}

// afterAt returns the path after " at " ("installed java 21.0.11+10 at /…/bin/java").
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

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
