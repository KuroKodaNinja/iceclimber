package remotefs

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/pkg/sftp"
)

// SFTPFS implements FS over an SFTP client — the fast path. pkg/sftp already maps
// "no such file" to os.ErrNotExist, so the wrapped errors satisfy
// errors.Is(err, fs.ErrNotExist) just like ExecFS.
type SFTPFS struct {
	c *sftp.Client
}

// NewSFTPFS returns an SFTPFS over c. The caller owns c's lifecycle.
func NewSFTPFS(c *sftp.Client) *SFTPFS {
	return &SFTPFS{c: c}
}

func (s *SFTPFS) Mkdir(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.c.MkdirAll(path); err != nil {
		return fmt.Errorf("sftpfs mkdir %s: %w", path, err)
	}
	return nil
}

func (s *SFTPFS) WriteFile(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := s.c.Create(path) // O_RDWR|O_CREATE|O_TRUNC
	if err != nil {
		return fmt.Errorf("sftpfs write %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("sftpfs write %s: %w", path, err)
	}
	return nil
}

func (s *SFTPFS) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := s.c.Open(path)
	if err != nil {
		return nil, fmt.Errorf("sftpfs read %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("sftpfs read %s: %w", path, err)
	}
	return data, nil
}

func (s *SFTPFS) List(ctx context.Context, dir string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := s.c.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("sftpfs list %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func (s *SFTPFS) Rename(ctx context.Context, oldpath, newpath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// PosixRename (posix-rename@openssh.com) atomically replaces an existing
	// target; plain Rename would fail if newpath exists.
	if err := s.c.PosixRename(oldpath, newpath); err != nil {
		return fmt.Errorf("sftpfs rename %s: %w", oldpath, err)
	}
	return nil
}

func (s *SFTPFS) Chmod(ctx context.Context, path string, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.c.Chmod(path, mode); err != nil {
		return fmt.Errorf("sftpfs chmod %s: %w", path, err)
	}
	return nil
}

func (s *SFTPFS) Symlink(ctx context.Context, target, link string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.c.Symlink(target, link); err != nil {
		return fmt.Errorf("sftpfs symlink %s: %w", link, err)
	}
	return nil
}
