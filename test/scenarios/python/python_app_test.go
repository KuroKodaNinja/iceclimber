//go:build scenario

// Package pythonapp is a self-contained, full-stack application scenario: it
// fetches data through Popo, provisions a Python runtime and the rich package in
// the sandbox, runs a real program that uses rich to process the fetched data, and
// asserts its computed output. The Python counterpart of the Node/Java scenarios.
// Run with `make scenario`.
package pythonapp

import (
	"context"
	_ "embed"
	"encoding/json"
	"path"
	"strconv"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/test/scenarios/harness"
)

//go:embed app/app.py
var appPy []byte

// TestPythonApp exercises the whole Python stack end to end: web.fetch (through
// Popo) → python.install → pip.install (rich, relay) → run a real program → assert
// it rendered the computed report.
func TestPythonApp(t *testing.T) {
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

	// 2. Provision Python + rich (relay).
	sb.Run(t, "install", "python", "3.12", "--config", cfg, "--transport", "sftp")
	pipOut := string(sb.Run(t, "install", "pip", "rich",
		"--python", "3.12", "--tier", "relay", "--config", cfg, "--transport", "sftp"))
	if !strings.Contains(pipOut, "installed rich") {
		t.Fatalf("pip install rich output:\n%s", pipOut)
	}

	// 3. Deploy + run the program with the installed interpreter.
	if err := fs.WriteFile(ctx, path.Join(work, "app.py"), appPy); err != nil {
		t.Fatalf("write app.py: %v", err)
	}
	py := strings.TrimSpace(sb.Sh(t, "ls "+root+"/runtimes/python/*/bin/python3 2>/dev/null | head -1"))
	if py == "" {
		t.Fatal("no installed python under runtimes/python")
	}
	out := sb.Sh(t, shq(py)+" "+shq(path.Join(work, "app.py"))+" "+shq(path.Join(work, "comic.json")))

	// 4. Assert the report carries the computed values (program ran, rich loaded,
	//    it processed the fetched data).
	num := strconv.Itoa(int(comic["num"].(float64)))
	title, _ := comic["title"].(string)
	titleLen := strconv.Itoa(len([]rune(title))) // Python len(title) = code points
	for _, want := range []string{num, title, titleLen} {
		if !strings.Contains(out, want) {
			t.Errorf("app output is missing %q:\n%s", want, out)
		}
	}
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
