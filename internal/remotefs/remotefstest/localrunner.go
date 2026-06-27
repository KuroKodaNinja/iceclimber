package remotefstest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"

	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

// LocalRunner runs commands through the host's /bin/sh, implementing
// remote.Runner without SSH. It lets ExecFS-backed code be tested fast and
// hermetically; on macOS that's a BSD userland — a second POSIX target beyond
// the VM's BusyBox, so it independently guards against GNU-isms.
type LocalRunner struct{}

// Run executes cmd via `sh -c`, streaming stdin when non-nil.
func (LocalRunner) Run(ctx context.Context, cmd string, stdin io.Reader) (remote.Result, error) {
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	if stdin != nil {
		c.Stdin = stdin
	}
	var out, errb bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errb
	err := c.Run()
	res := remote.Result{Stdout: out.Bytes(), Stderr: errb.Bytes()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	return res, err
}

// Close is a no-op.
func (LocalRunner) Close() error { return nil }
