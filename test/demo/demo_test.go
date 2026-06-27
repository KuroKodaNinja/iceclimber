//go:build demo

// Package demo holds the air-gapped agent acceptance demo (DEMO.md). It is gated
// behind the `demo` build tag, so plain `go test ./...` never touches it.
package demo

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAcceptanceDemo runs the full demo end to end: a real Claude agent, sealed
// in a sandbox that can reach only the Claude API, uses iceclimber to provision
// Python + rich + web data through Popo, builds a program, and runs it. We assert
// the program renders the data it fetched.
//
// Opt-in: needs CLAUDE_CODE_OAUTH_TOKEN (a *subscription* token from
// `claude setup-token`, never the metered API) and the provisioned demo VM
// (`make demo-up`). The heavy lifting lives in test/lima/demo-run.sh; this test
// gates, runs it, and checks the verdict.
func TestAcceptanceDemo(t *testing.T) {
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" {
		t.Skip("set CLAUDE_CODE_OAUTH_TOKEN (claude setup-token) to run the demo — subscription, not API")
	}
	if _, err := exec.LookPath("limactl"); err != nil {
		t.Skip("limactl not found; install Lima and run `make demo-up`")
	}
	if out, err := exec.Command("limactl", "list", "--quiet").Output(); err != nil ||
		!strings.Contains(string(out), "iceclimber-demo") {
		t.Skip("demo VM not found; run `make demo-up`")
	}

	repo := repoRoot()
	cmd := exec.Command("bash", filepath.Join(repo, "test", "lima", "demo-run.sh"), "iceclimber-demo")
	cmd.Dir = repo
	cmd.Env = os.Environ() // forward CLAUDE_CODE_OAUTH_TOKEN
	out, err := cmd.CombinedOutput()
	t.Logf("demo-run output:\n%s", out)
	if err != nil {
		t.Fatalf("demo run failed: %v", err)
	}
	if !strings.Contains(string(out), "DEMO VERIFY: PASS") {
		t.Fatal("demo did not reach a PASS verdict")
	}
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}
