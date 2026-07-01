//go:build scenario

package pythonapp

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/test/scenarios/harness"
)

//go:embed app/environment.yml
var envYML []byte

//go:embed app/ml.py
var mlPy []byte

// TestPythonCondaApp is the heavyweight conda counterpart to TestPythonApp: a real
// conda project (environment.yml pinning python + pytorch + pandas + numpy) built into
// a conda env by the air-gapped relay — the controller's conda/mamba solves the env for
// the sandbox platform, downloads it, pushes a local channel, and the sandbox creates the
// env OFFLINE — then a real PyTorch + pandas program runs against it. PyTorch has no musl
// build, so this runs on the glibc box; it skips without conda/mamba on the controller.
func TestPythonCondaApp(t *testing.T) {
	controllerConda := controllerCondaBin()
	if controllerConda == "" {
		t.Skip("conda relay needs conda or mamba on the controller")
	}
	sb := harness.RequireGlibc(t)
	root := sb.NewRoot(t)
	cfg := sb.WriteConfig(t, root, fmt.Sprintf(`controller_conda: %s
runtimes:
  python:
    source: system
    env_manager: conda
network:
  allowed_domains:
    - pattern: "xkcd.com"
      reachable_from: sandbox`, controllerConda))

	sb.Run(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	fs := sb.DialFS(t, "sftp")
	ctx := context.Background()
	proj := path.Join(root, "mlproject")
	if err := fs.Mkdir(ctx, proj); err != nil {
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

	// 2. Deploy the environment.yml + program, and build the whole conda env from the
	//    manifest via the air-gapped relay.
	if err := fs.WriteFile(ctx, path.Join(proj, "environment.yml"), envYML); err != nil {
		t.Fatalf("write environment.yml: %v", err)
	}
	if err := fs.WriteFile(ctx, path.Join(proj, "ml.py"), mlPy); err != nil {
		t.Fatalf("write ml.py: %v", err)
	}
	condaOut := string(sb.Run(t, "install", "conda", "--file", path.Join(proj, "environment.yml"),
		"--tier", "relay", "--config", cfg, "--transport", "sftp"))
	for _, want := range []string{"installed pytorch", "installed pandas", "installed numpy"} {
		if !strings.Contains(condaOut, want) {
			t.Fatalf("conda --file install output (want torch/pandas/numpy):\n%s", condaOut)
		}
	}

	// 3. Run the program with the env's interpreter (the manifest's env name → mlkit).
	py := path.Join(root, "envs", "mlkit", "bin", "python")
	out := sb.Sh(t, fmt.Sprintf("%s %s %s", shq(py), shq(path.Join(proj, "ml.py")), shq(path.Join(proj, "comic.json"))))

	// 4. Assert torch + pandas loaded, ran, and processed the fetched data.
	num := strconv.Itoa(int(comic["num"].(float64)))
	title, _ := comic["title"].(string)
	titleLen := strconv.Itoa(len([]rune(title))) // Python len(title) = code points
	for _, want := range []string{"MLKIT_OK", num, title, "title length: " + titleLen} {
		if !strings.Contains(out, want) {
			t.Errorf("app output is missing %q:\n%s", want, out)
		}
	}
}

// controllerCondaBin returns the controller's real conda/mamba executable (mamba is a
// drop-in; a `conda` shell alias won't do — the relay execs it), or "" if neither is
// present.
func controllerCondaBin() string {
	for _, b := range []string{"conda", "mamba"} {
		if _, err := exec.LookPath(b); err == nil && exec.Command(b, "--version").Run() == nil {
			return b
		}
	}
	return ""
}
