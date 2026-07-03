//go:build functional

package functional

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestSetupHeadless drives the guided first run end to end in its scriptable form,
// from a genuinely fresh state: an empty working dir (no config), a private HOME (so
// the recorded host key and controller state never touch the operator's real
// ~/.ssh/known_hosts), and --yes. One command must scaffold the config, trust the key,
// bootstrap the sandbox, and install the shell; a second run must be idempotent.
func TestSetupHeadless(t *testing.T) {
	sb := requireSandbox(t)
	home := t.TempDir()
	dir := t.TempDir()

	run := func() string {
		t.Helper()
		cmd := exec.Command(iceclimberBin, "setup", "--yes", "--skip-agent", "--no-console",
			"--host", sb.Host, "--user", sb.User, "--port", itoa(sb.Port),
			"--identity-file", sb.IdentityFile, "--id", "setuptest")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("setup: %v\n%s", err, out)
		}
		return string(out)
	}

	out := run()
	for _, want := range []string{"[1] config", "bootstrapped at", "bash", "setup complete"} {
		if !strings.Contains(out, want) {
			t.Errorf("setup output missing %q:\n%s", want, out)
		}
	}
	// The trust step ran: it either recorded the key or found it already trusted (the
	// lima box is in the resolved known_hosts on a dev machine — every suite dials it).
	if !strings.Contains(out, "recorded host key") && !strings.Contains(out, "already trusted") {
		t.Errorf("setup's trust step left no trace:\n%s", out)
	}
	// The config was scaffolded in the working dir with the requested id.
	cfgBytes, err := os.ReadFile(filepath.Join(dir, "iceclimber.yaml"))
	if err != nil {
		t.Fatalf("scaffolded config: %v", err)
	}
	if !strings.Contains(string(cfgBytes), "sandbox_id: setuptest") {
		t.Errorf("scaffolded config missing the sandbox id:\n%s", cfgBytes)
	}

	// The sandbox really is bootstrapped: NANA.md + popo at the root setup reported.
	root := ""
	if m := regexp.MustCompile(`bootstrapped at (\S+)`).FindStringSubmatch(out); m != nil {
		root = m[1]
	}
	if root == "" {
		t.Fatalf("could not find the chosen root in setup output:\n%s", out)
	}
	if strings.Contains(root, "iceclimber") { // safety: only clean a dedicated dir
		t.Cleanup(func() { limaSh(t, "rm -rf "+root) })
	}
	if s := limaSh(t, "test -f "+root+"/skill/NANA.md && test -x "+root+"/popo && echo ok"); !strings.Contains(s, "ok") {
		t.Errorf("sandbox not actually bootstrapped at %s", root)
	}
	if s := limaSh(t, root+"/shell/bash --version 2>&1 | head -1"); !strings.Contains(s, "GNU bash") {
		t.Errorf("shell step did not install a runnable bash: %q", s)
	}

	// Idempotent: a second run reuses the config, finds the key trusted, and re-bootstraps
	// safely (runtimes/shell preserved — the shell step reports already installed).
	out2 := run()
	for _, want := range []string{"already trusted", "bootstrapped at", "setup complete"} {
		if !strings.Contains(out2, want) {
			t.Errorf("second setup run missing %q:\n%s", want, out2)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
