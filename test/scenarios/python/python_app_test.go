//go:build scenario

// Package pythonapp holds the Python application scenarios: a pip-relay build on the
// musl box (pandas + numpy) and a conda-relay build on the glibc box (pytorch + pandas,
// see conda_app_test.go). Each fetches data through Popo, provisions a runtime and
// packages, runs a real program that processes the fetched data, and asserts its output.
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

// TestPythonApp exercises the pip-relay Python stack end to end on the musl box:
// web.fetch (through Popo) → python.install → pip.install (pandas + numpy — real
// C-extension packages with musllinux wheels, relayed) → run a real pandas program →
// assert the computed report. (The conda torch+pandas build is TestPythonCondaApp.)
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

	// 2. Provision Python + pandas (relay — pulls numpy + deps as tag-matched musllinux
	//    wheels the controller downloads and relays in).
	sb.Run(t, "install", "python", "3.12", "--config", cfg, "--transport", "sftp")
	pipOut := string(sb.Run(t, "install", "pip", "pandas",
		"--python", "3.12", "--tier", "relay", "--config", cfg, "--transport", "sftp"))
	if !strings.Contains(pipOut, "installed pandas") {
		t.Fatalf("pip install pandas output:\n%s", pipOut)
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
	for _, want := range []string{"PANDAS_OK", num, title, "title length: " + titleLen} {
		if !strings.Contains(out, want) {
			t.Errorf("app output is missing %q:\n%s", want, out)
		}
	}
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
