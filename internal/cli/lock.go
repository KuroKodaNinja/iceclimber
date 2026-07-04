package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/xdg"
)

// Single-writer enforcement (plan §9's `.popo.lock` model, revised for proxy egress) —
// two controller-side advisory flocks per sandbox, under ~/.config/iceclimber/<sandbox_id>/:
//
//   - serve.lock — exclusive among SERVING processes (serve / console / headless serve),
//     held for the whole run (spanning reconnects). Two dispatchers on one maildir would
//     double-service requests; a second serve/console fails fast naming the holder.
//   - ops.lock — exclusive among MUTATING ONE-SHOTS (install / bootstrap / agent install /
//     maven build), held for the command's duration. Two concurrent CLI installs could
//     race the same runtime env, so the second fails fast.
//
// A mutating one-shot during an ACTIVE SERVE is deliberately ALLOWED (with a stderr
// notice naming the serving process): plan §9 originally decided fail-fast, but proxy
// egress (#76–#78) made coexistence a REQUIREMENT — the proxy only exists inside a
// serving process, so e.g. `maven build` in proxy mode must run alongside serve. The
// dispatcher's keyed runtime locks plus the process-safe log sinks make the overlap
// tolerable; cross-process runtime serialization can ride on the shared relay layer later
// if it proves needed. Read-only commands (status, logs, probe, shell, web fetch) take no
// lock. flock is released by the kernel when the holder dies, so a crashed process never
// wedges a sandbox; the holder text in the file is best-effort context, not the truth.

// serveLockPath / opsLockPath are the per-sandbox lock files (alongside activity.jsonl).
// Empty when no home directory is resolvable — locking is then skipped (matching the log
// sinks' behavior).
func serveLockPath(cfg *config.Config) string { return lockFile(cfg, "serve.lock") }
func opsLockPath(cfg *config.Config) string   { return lockFile(cfg, "ops.lock") }

func lockFile(cfg *config.Config, name string) string {
	return filepath.Join(xdg.SandboxDir(cfg.SandboxID), name)
}

// sandboxLock is a held lock; Release is idempotent and nil-safe.
type sandboxLock struct {
	f *os.File
}

// acquireServeLock takes the per-sandbox SERVING lock (serve/console), or fails fast
// naming the current holder. mode records this process's role for the peer's message.
func acquireServeLock(cfg *config.Config, mode string) (*sandboxLock, error) {
	lk, holder, err := tryFlock(serveLockPath(cfg), mode)
	if err != nil {
		return nil, err
	}
	if lk == nil {
		return nil, fmt.Errorf("sandbox %q is already being served%s — one serving process per sandbox; stop it first",
			cfg.SandboxID, holder)
	}
	return lk, nil
}

// acquireOpsLock takes the per-sandbox MUTATING-ONE-SHOT lock, or fails fast naming the
// concurrent one-shot. It also probes the serve lock: an active serving process does NOT
// block the one-shot (proxy-mode flows require the coexistence — see the package comment),
// but the returned notice names it so the caller can tell the operator.
func acquireOpsLock(cfg *config.Config, mode string) (lk *sandboxLock, notice string, err error) {
	lk, holder, err := tryFlock(opsLockPath(cfg), mode)
	if err != nil {
		return nil, "", err
	}
	if lk == nil {
		return nil, "", fmt.Errorf("sandbox %q is already in use%s — one mutating command at a time (a serving console installs via its own `i` form; the agent via `popo`)",
			cfg.SandboxID, holder)
	}
	if h := probeHolder(serveLockPath(cfg)); h != "" {
		notice = fmt.Sprintf("note: sandbox %q is being served%s — Popo may operate on it concurrently", cfg.SandboxID, h)
	}
	return lk, notice, nil
}

// tryFlock attempts a non-blocking exclusive flock on path, writing holder context (pid ·
// mode · start) on success. Returns (nil, holderText, nil) when the lock is already held.
// An empty path (no home dir) yields a no-op lock — consistent with the log sinks.
func tryFlock(path, mode string) (*sandboxLock, string, error) {
	if path == "" {
		return &sandboxLock{}, "", nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, "", err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := readHolder(f)
		_ = f.Close()
		return nil, holder, nil
	}
	// Record holder context for the peer's error/notice message (best-effort).
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = fmt.Fprintf(f, "pid %d · %s · since %s\n", os.Getpid(), mode, time.Now().Format("15:04:05"))
	_ = f.Sync()
	return &sandboxLock{f: f}, "", nil
}

// probeHolder reports the holder text of an exclusively-held lock at path ("" when free,
// absent, or unreadable). It takes and immediately drops a shared flock probe.
func probeHolder(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return "" // never locked / no home — free
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return "" // shared probe succeeded → nobody holds it exclusively
	}
	return readHolder(f)
}

// readHolder formats the holder line written by the current owner, e.g.
// " by pid 1234 · serve · since 12:03:05". Best-effort: empty on any read problem.
func readHolder(f *os.File) string {
	buf := make([]byte, 256)
	n, err := f.ReadAt(buf, 0)
	if n == 0 && err != nil {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(string(buf[:n]), "\n", 2)[0])
	if line == "" {
		return ""
	}
	return " by " + line
}

// Release drops the lock. Idempotent; safe on the no-op lock and on nil.
func (l *sandboxLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
