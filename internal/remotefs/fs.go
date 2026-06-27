// Package remotefs provides a small filesystem abstraction over the sandbox
// host, with two interchangeable transports: SFTPFS (the fast path) and ExecFS
// (the fallback for hosts whose SFTP subsystem is disabled, plan §6). A single
// conformance suite (subpackage remotefstest) asserts both behave identically,
// so nothing above this layer ever knows which is active.
package remotefs

import "context"

// FS is the transport-agnostic filesystem the protocol layer runs on. All paths
// are absolute (the absolute-path contract, plan §2). Operations on a missing
// path return an error satisfying errors.Is(err, fs.ErrNotExist), regardless of
// transport.
type FS interface {
	// Mkdir creates dir and any missing parents (mkdir -p / MkdirAll).
	Mkdir(ctx context.Context, path string) error
	// WriteFile writes data to path, creating or truncating it.
	WriteFile(ctx context.Context, path string, data []byte) error
	// ReadFile returns the contents of path.
	ReadFile(ctx context.Context, path string) ([]byte, error)
	// List returns the sorted basenames in dir. An empty directory yields an
	// empty slice and a nil error; a missing directory is an error.
	List(ctx context.Context, dir string) ([]string, error)
	// Rename moves oldpath to newpath with POSIX replace semantics (an existing
	// newpath is atomically replaced).
	Rename(ctx context.Context, oldpath, newpath string) error
}
