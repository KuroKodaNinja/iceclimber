package pip

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

func TestParseReport(t *testing.T) {
	const report = `{
      "version": "1",
      "install": [
        {"metadata": {"name": "six", "version": "1.16.0"},
         "download_info": {"url": "https://f/six.whl", "archive_info": {"hashes": {"sha256": "abc123"}}}},
        {"metadata": {"name": "legacy", "version": "0.1"},
         "download_info": {"url": "https://f/legacy.tar.gz", "archive_info": {}}}
      ]
    }`
	plan, err := parseReport([]byte(report))
	if err != nil {
		t.Fatalf("parseReport: %v", err)
	}
	if len(plan.Packages) != 2 {
		t.Fatalf("got %d packages, want 2", len(plan.Packages))
	}
	if p := plan.Packages[0]; p.Name != "six" || p.Version != "1.16.0" || p.SHA256 != "abc123" {
		t.Errorf("pkg[0] = %+v", p)
	}
	if p := plan.Packages[1]; p.Name != "legacy" || p.SHA256 != "" { // sdist: no hash, must not crash
		t.Errorf("pkg[1] = %+v, want empty sha", p)
	}
}

func TestSpecString(t *testing.T) {
	if got := specString(pkg.Spec{Name: "requests", Version: "2.32.3"}); got != "requests==2.32.3" {
		t.Errorf("versioned = %q", got)
	}
	if got := specString(pkg.Spec{Name: "requests"}); got != "requests" {
		t.Errorf("unversioned = %q", got)
	}
}

func TestResolveCmd(t *testing.T) {
	m := New(Config{PythonBin: "/r/bin/python3", IndexURL: "https://idx/simple", TrustedHost: "idx"})
	cmd := m.resolveCmd([]pkg.Spec{{Name: "a", Version: "1"}, {Name: "b"}}, "/state/rep.json")
	for _, want := range []string{
		"'/r/bin/python3' -m pip install", "--dry-run", "--report '/state/rep.json'",
		"--index-url 'https://idx/simple'", "--trusted-host 'idx'", "'a==1'", "'b'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("resolveCmd missing %q in:\n%s", want, cmd)
		}
	}
}

// scriptRunner is a fake remote.Runner driven by a function.
type scriptRunner struct {
	fn func(cmd string) (remote.Result, error)
}

func (s scriptRunner) Run(_ context.Context, cmd string, _ io.Reader) (remote.Result, error) {
	return s.fn(cmd)
}
func (s scriptRunner) Close() error { return nil }

func TestInstall_ReportsPerPackageProgress(t *testing.T) {
	var events []progress.Event
	m := New(Config{
		PythonBin: "/py",
		IndexURL:  "https://idx/simple",
		Runner:    scriptRunner{fn: func(string) (remote.Result, error) { return remote.Result{}, nil }},
		Progress:  func(e progress.Event) { events = append(events, e) },
	})
	plan := pkg.Plan{Packages: []pkg.Resolved{{Name: "requests", Version: "2.0"}, {Name: "urllib3", Version: "2.2"}}}
	if _, err := m.Install(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d progress events, want one per package", len(events))
	}
	if events[0].Phase != "installing requests" || events[0].Cur != 1 || events[0].Total != 2 || events[0].Unit != progress.Items {
		t.Errorf("first event = %+v, want installing requests (1/2) Items", events[0])
	}
	if events[1].Cur != 2 || events[1].Phase != "installing urllib3" {
		t.Errorf("second event = %+v, want installing urllib3 (2/2)", events[1])
	}
}

func TestInstall_PerPackageOutcome(t *testing.T) {
	m := New(Config{
		PythonBin: "/py",
		IndexURL:  "https://idx/simple",
		Runner: scriptRunner{fn: func(cmd string) (remote.Result, error) {
			if !strings.Contains(cmd, "--no-deps") {
				t.Errorf("install cmd should pass --no-deps: %s", cmd)
			}
			switch {
			case strings.Contains(cmd, "good==1.0"):
				return remote.Result{}, nil // exit 0
			case strings.Contains(cmd, "bad==2.0"):
				return remote.Result{ExitCode: 1, Stderr: []byte("ERROR: No matching distribution found for bad==2.0")}, nil
			}
			return remote.Result{}, nil
		}},
	})
	plan := pkg.Plan{Packages: []pkg.Resolved{
		{Name: "good", Version: "1.0", SHA256: "aaa"},
		{Name: "bad", Version: "2.0"},
	}}
	out, err := m.Install(context.Background(), plan)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(out.Installed) != 1 || out.Installed[0].Name != "good" || out.Installed[0].Tier != pkg.TierMirror || out.Installed[0].SHA256 != "aaa" {
		t.Errorf("Installed = %+v", out.Installed)
	}
	if len(out.Failed) != 1 || out.Failed[0].Name != "bad" || !strings.Contains(out.Failed[0].Error, "No matching distribution") {
		t.Errorf("Failed = %+v", out.Failed)
	}
}
