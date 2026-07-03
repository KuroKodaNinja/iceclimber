package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
)

// TestServeLock exercises the serving single-writer contract: a held serve lock refuses a
// second serving process with a message naming the holder; release frees it; distinct
// sandboxes never contend. flock is per open-file-description, so two acquires in one
// process contend exactly like two processes.
func TestServeLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{SandboxID: "locktest"}

	lk, err := acquireServeLock(cfg, "serve")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// A second serving process fails fast, naming the sandbox and the holder's pid + mode.
	_, err = acquireServeLock(cfg, "console")
	if err == nil {
		t.Fatal("second serve acquire should fail while the lock is held")
	}
	for _, want := range []string{"locktest", "already being served", "serve", "pid"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("contention error %q missing %q", err.Error(), want)
		}
	}

	// A different sandbox does NOT contend (per-sandbox lock — the fleet invariant).
	other := &config.Config{SandboxID: "locktest-other"}
	lk2, err := acquireServeLock(other, "serve")
	if err != nil {
		t.Fatalf("distinct sandbox should not contend: %v", err)
	}
	lk2.Release()

	// Release frees it; a fresh acquire then succeeds and records the new holder.
	lk.Release()
	lk3, err := acquireServeLock(cfg, "console")
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if data, _ := os.ReadFile(serveLockPath(cfg)); !strings.Contains(string(data), "console") {
		t.Errorf("lock file should record the new holder's mode; got %q", data)
	}
	lk3.Release()
	lk3.Release() // idempotent
}

// TestOpsLock: mutating one-shots are exclusive among THEMSELVES, but an active SERVING
// process does not block them — it surfaces as a notice instead (proxy-mode flows, e.g.
// `maven build --tier proxy`, REQUIRE running alongside serve; see lock.go).
func TestOpsLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{SandboxID: "opslock"}

	// No serve active → acquire succeeds with no notice.
	lk, notice, err := acquireOpsLock(cfg, "install")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if notice != "" {
		t.Errorf("no serve is active; unexpected notice %q", notice)
	}

	// A second one-shot fails fast naming the first.
	if _, _, err := acquireOpsLock(cfg, "bootstrap"); err == nil ||
		!strings.Contains(err.Error(), "already in use") || !strings.Contains(err.Error(), "install") {
		t.Fatalf("second one-shot should fail naming the first; got %v", err)
	}
	lk.Release()

	// With a SERVING process holding the serve lock, a one-shot still succeeds — with a
	// notice naming the server. (The old fail-fast here would break proxy-mode maven build.)
	serve, err := acquireServeLock(cfg, "serve")
	if err != nil {
		t.Fatal(err)
	}
	defer serve.Release()
	lk2, notice, err := acquireOpsLock(cfg, "maven build")
	if err != nil {
		t.Fatalf("one-shot during serve must be allowed (proxy-mode flows depend on it): %v", err)
	}
	defer lk2.Release()
	if !strings.Contains(notice, "being served") || !strings.Contains(notice, "serve") {
		t.Errorf("notice should name the serving process; got %q", notice)
	}
}

// TestOpenLockedSessionContention: openLockedSession must fail fast (no dial) when another
// mutating one-shot holds the ops lock — the error is the lock's, not a connection error.
func TestOpenLockedSessionContention(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{SandboxID: "locktest2"}

	lk, _, err := acquireOpsLock(cfg, "install")
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()

	// cfg has no SSH host — if openLockedSession dialed, the error would be a dial error.
	// It must instead be the single-writer refusal, proving the lock check precedes the dial.
	_, err = openLockedSession(t.Context(), cfg, "auto", "install")
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("want the fast single-writer refusal before any dial, got: %v", err)
	}
}
