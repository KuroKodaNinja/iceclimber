// Package logfile is a rotating, append-only log writer that is safe for concurrent goroutines
// AND concurrent OS processes writing the same file. It is the single home for the controller's
// three log sinks (activity, egress audit, the bridged agent stream) and the sandbox-side
// session log (via `popo log`), so log rotation + write-safety are defined once.
//
// Safety model:
//   - Appends open the file with O_APPEND and write each record in one write(2). POSIX guarantees
//     each O_APPEND write lands at EOF atomically for records under the filesystem's atomic-write
//     size (log lines qualify), so concurrent appenders — goroutines or processes — never tear or
//     lose a line; they only interleave, which is fine for a log.
//   - Rotation is coordinated across processes with an advisory flock on a sidecar ".lock" file
//     and an on-disk size check (fstat of the live fd, never a stale in-memory counter), so two
//     processes can't both rotate or clobber. A process whose fd was rotated out from under it by
//     another process detects the swap (the path no longer names its inode) and reopens.
package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// Defaults applied when New receives a non-positive bound.
const (
	DefaultMaxBytes int64 = 8 << 20 // 8 MiB per generation
	DefaultKeep           = 4       // path + path.1 .. path.(keep-1) → ~keep generations
)

// Writer is a rotating append-only writer. The zero-value path ("") disables it (Append is a
// no-op), mirroring the existing "empty path = logging off" convention.
type Writer struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	f        *os.File
}

// New builds a Writer. maxBytes<=0 and keep<1 fall back to the defaults.
func New(path string, maxBytes int64, keep int) *Writer {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if keep < 1 {
		keep = 1
	}
	return &Writer{path: path, maxBytes: maxBytes, keep: keep}
}

// Append writes one record (typically a single line incl. its trailing newline) atomically to the
// current generation, rotating first if the file is at/over the size cap. Safe for concurrent
// goroutines and processes.
func (w *Writer) Append(b []byte) error {
	if w.path == "" {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureOpen(); err != nil {
		return err
	}
	if err := w.rotateIfNeeded(); err != nil {
		return err
	}
	_, err := w.f.Write(b)
	return err
}

// Close releases the open fd. Idempotent.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

func (w *Writer) ensureOpen() error {
	if w.f != nil {
		return nil
	}
	if dir := filepath.Dir(w.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w.f = f
	return nil
}

func (w *Writer) reopen() error {
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
	return w.ensureOpen()
}

// rotateIfNeeded follows an external rotation (our fd no longer names `path`) and rotates when the
// live file is at/over the cap. Called under w.mu with w.f open.
func (w *Writer) rotateIfNeeded() error {
	fdInfo, err := w.f.Stat()
	if err != nil {
		return err
	}
	pathInfo, statErr := os.Stat(w.path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return w.reopen() // rotated away / removed under us → recreate
		}
		return statErr
	}
	if !os.SameFile(fdInfo, pathInfo) {
		return w.reopen() // another process rotated → follow the new file
	}
	if fdInfo.Size() < w.maxBytes {
		return nil
	}
	return w.rotateLocked()
}

// rotateLocked shifts generations under a cross-process advisory lock, double-checking the size so
// two processes racing to rotate do it once.
func (w *Writer) rotateLocked() error {
	lk, err := acquireLock(w.path)
	if err != nil {
		return err
	}
	defer lk.release()

	// Another process may have rotated while we waited for the lock — if so, just follow it.
	if fi, err := os.Stat(w.path); err == nil && fi.Size() < w.maxBytes {
		return w.reopen()
	}
	if w.keep <= 1 {
		// No history kept: start the current file over (transparent to other appenders — their
		// O_APPEND fds simply resume at the new EOF of 0).
		if err := os.Truncate(w.path, 0); err != nil && !os.IsNotExist(err) {
			return err
		}
		return w.reopen()
	}
	// Keep generations {path, path.1 .. path.(keep-1)}: drop the oldest, shift each down, and the
	// current file becomes path.1.
	_ = os.Remove(genName(w.path, w.keep-1))
	for i := w.keep - 2; i >= 1; i-- {
		_ = os.Rename(genName(w.path, i), genName(w.path, i+1))
	}
	if err := os.Rename(w.path, genName(w.path, 1)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return w.reopen()
}

// genName is "<path>.<n>" for a rotated generation.
func genName(path string, n int) string { return fmt.Sprintf("%s.%d", path, n) }

// flock is a held cross-process advisory lock on "<path>.lock".
type flock struct{ f *os.File }

func acquireLock(logpath string) (*flock, error) {
	lp := logpath + ".lock"
	if dir := filepath.Dir(lp); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	f, err := os.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &flock{f: f}, nil
}

func (l *flock) release() {
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}
