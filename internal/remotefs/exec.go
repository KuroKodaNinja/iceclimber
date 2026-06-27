package remotefs

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

// ExecFS implements FS using only the pinned POSIX-sh command palette over a
// remote.Runner (plan §6): mkdir -p, cat, cat > f (stdin), ls -1, mv. No
// bashisms, no GNU-only flags, no stat — so it works on BusyBox.
type ExecFS struct {
	r remote.Runner
}

// NewExecFS returns an ExecFS over r.
func NewExecFS(r remote.Runner) *ExecFS {
	return &ExecFS{r: r}
}

func (e *ExecFS) Mkdir(ctx context.Context, path string) error {
	res, err := e.r.Run(ctx, "mkdir -p "+remote.ShellQuote(path), nil)
	return e.check("mkdir", path, res, err)
}

func (e *ExecFS) WriteFile(ctx context.Context, path string, data []byte) error {
	// Raw stream into `cat > path` — no base64 (plan §6). The shell performs the
	// redirection, so a missing parent surfaces as a "no such file" error.
	res, err := e.r.Run(ctx, "cat > "+remote.ShellQuote(path), bytes.NewReader(data))
	return e.check("write", path, res, err)
}

func (e *ExecFS) ReadFile(ctx context.Context, path string) ([]byte, error) {
	res, err := e.r.Run(ctx, "cat "+remote.ShellQuote(path), nil)
	if err != nil {
		return nil, fmt.Errorf("execfs read %s: %w", path, err)
	}
	if res.ExitCode != 0 {
		return nil, e.statusErr("read", path, res)
	}
	return res.Stdout, nil
}

func (e *ExecFS) List(ctx context.Context, dir string) ([]string, error) {
	res, err := e.r.Run(ctx, "ls -1 "+remote.ShellQuote(dir), nil)
	if err != nil {
		return nil, fmt.Errorf("execfs list %s: %w", dir, err)
	}
	if res.ExitCode != 0 {
		return nil, e.statusErr("list", dir, res)
	}
	names := splitLines(res.Stdout)
	sort.Strings(names)
	return names, nil
}

func (e *ExecFS) Rename(ctx context.Context, oldpath, newpath string) error {
	// POSIX mv replaces an existing target atomically (same filesystem).
	res, err := e.r.Run(ctx, "mv "+remote.ShellQuote(oldpath)+" "+remote.ShellQuote(newpath), nil)
	return e.check("rename", oldpath, res, err)
}

func (e *ExecFS) check(op, path string, res remote.Result, err error) error {
	if err != nil {
		return fmt.Errorf("execfs %s %s: %w", op, path, err)
	}
	if res.ExitCode != 0 {
		return e.statusErr(op, path, res)
	}
	return nil
}

// statusErr maps a non-zero exit to fs.ErrNotExist when the stderr indicates a
// missing path, so callers can errors.Is the same way they would for SFTP.
func (e *ExecFS) statusErr(op, path string, res remote.Result) error {
	if strings.Contains(strings.ToLower(string(res.Stderr)), "no such file") {
		return fmt.Errorf("execfs %s %s: %w", op, path, fs.ErrNotExist)
	}
	return fmt.Errorf("execfs %s %s: exit %d: %s", op, path, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
}

// splitLines splits ls -1 output into basenames, dropping a trailing newline and
// any empties (an empty directory yields no lines).
func splitLines(b []byte) []string {
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	for i := range parts {
		parts[i] = strings.TrimRight(parts[i], "\r")
	}
	return parts
}
