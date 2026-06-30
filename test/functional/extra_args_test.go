//go:build functional

package functional

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestPipExtraArgsPassthrough proves agent-supplied pip flags reach pip — on the musl
// box with a musllinux-available package (the mechanism is libc-agnostic; PyTorch on
// glibc is a separate test). The config sets NO index and we force --tier mirror, so a
// Tier-0 resolve only has an index if the agent's --index-url is forwarded — making
// success itself the proof. A disallowed flag must be rejected.
func TestPipExtraArgsPassthrough(t *testing.T) {
	sb := requireSandbox(t)
	root := "/tmp/iceclimber-extra-" + protocol.NewID()
	cfg := writeConfigRoot(t, sb, root) // no pip.index_url configured

	runIceclimber(t, "bootstrap", "--config", cfg, "--transport", "sftp")
	runIceclimber(t, "install", "python", "3.12", "--config", cfg, "--transport", "sftp")

	// Positive: --tier mirror with no config index succeeds only because the agent's
	// --index-url is forwarded into pip's resolve/install.
	out := runIceclimber(t, "install", "pip", "markupsafe==2.1.5",
		"--config", cfg, "--transport", "sftp", "--python", "3.12", "--tier", "mirror",
		"--pip-arg=--index-url", "--pip-arg=https://pypi.org/simple")
	if !strings.Contains(strings.ToLower(string(out)), "markupsafe") {
		t.Errorf("expected markupsafe installed via forwarded --index-url:\n%s", out)
	}
	imp := `for d in ` + root + `/runtimes/python/*/bin/python3; do "$d" -c 'import markupsafe; print(markupsafe.__version__)'; done`
	if got := limaSh(t, imp); !strings.Contains(got, "2.1.5") {
		t.Errorf("import markupsafe = %q, want 2.1.5", got)
	}

	// Negative: a flag outside the allowlist is rejected before anything runs.
	var stderr bytes.Buffer
	bad := exec.Command(iceclimberBin, "install", "pip", "markupsafe",
		"--config", cfg, "--transport", "sftp", "--python", "3.12",
		"--pip-arg=--no-such-flag")
	bad.Stderr = &stderr
	if err := bad.Run(); err == nil {
		t.Error("a disallowed --pip-arg should fail the install")
	}
	if !strings.Contains(stderr.String(), "not allowed") {
		t.Errorf("expected an allowlist rejection, got: %s", stderr.String())
	}
}
