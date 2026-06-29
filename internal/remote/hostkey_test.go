package remote

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// testKey makes a deterministic-ish ssh.PublicKey from a fresh ed25519 key.
func testKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestCheckHostKey_UnknownTrustedMismatch(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	key := testKey(t)
	other := testKey(t)

	// Missing file → unknown (not an error).
	if st, err := CheckHostKey(kh, "host.example", 2222, key); err != nil || st != TrustUnknown {
		t.Fatalf("missing file: state=%v err=%v, want TrustUnknown", st, err)
	}

	// Record then it's trusted.
	if err := RecordHostKey(kh, "host.example", 2222, key, false); err != nil {
		t.Fatalf("record: %v", err)
	}
	if st, err := CheckHostKey(kh, "host.example", 2222, key); err != nil || st != TrustTrusted {
		t.Fatalf("after record: state=%v err=%v, want TrustTrusted", st, err)
	}

	// A different key for the same host → mismatch.
	if st, err := CheckHostKey(kh, "host.example", 2222, other); err != nil || st != TrustMismatch {
		t.Fatalf("different key: state=%v err=%v, want TrustMismatch", st, err)
	}

	// A different host is still unknown.
	if st, err := CheckHostKey(kh, "other.example", 2222, key); err != nil || st != TrustUnknown {
		t.Fatalf("different host: state=%v err=%v, want TrustUnknown", st, err)
	}
}

func TestRecordHostKey_CreatesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "nested", "known_hosts") // dir does not exist yet
	key1 := testKey(t)
	key2 := testKey(t)

	if err := RecordHostKey(kh, "h", 22, key1, false); err != nil {
		t.Fatalf("record1: %v", err)
	}
	if fi, err := os.Stat(kh); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("known_hosts perms: %v, %v; want 0600", fi, err)
	}

	// Without --replace, a second key for the same host appends → now a mismatch
	// is ambiguous, so verifying key1 still matches (one of the two lines).
	if err := RecordHostKey(kh, "h", 22, key2, false); err != nil {
		t.Fatalf("record2 append: %v", err)
	}
	if st, _ := CheckHostKey(kh, "h", 22, key1); st != TrustTrusted {
		t.Errorf("key1 should still match after append, got %v", st)
	}

	// With --replace, only key2 remains.
	if err := RecordHostKey(kh, "h", 22, key2, true); err != nil {
		t.Fatalf("record replace: %v", err)
	}
	if st, _ := CheckHostKey(kh, "h", 22, key1); st != TrustMismatch {
		t.Errorf("key1 should be a mismatch after replace, got %v", st)
	}
	if st, _ := CheckHostKey(kh, "h", 22, key2); st != TrustTrusted {
		t.Errorf("key2 should be trusted after replace, got %v", st)
	}
}

func TestRecordHostKey_PreservesOtherHosts(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	keep := testKey(t)
	target := testKey(t)
	newKey := testKey(t)

	if err := RecordHostKey(kh, "keep.example", 22, keep, false); err != nil {
		t.Fatal(err)
	}
	if err := RecordHostKey(kh, "target.example", 22, target, false); err != nil {
		t.Fatal(err)
	}
	// Replace target.example only; keep.example must survive.
	if err := RecordHostKey(kh, "target.example", 22, newKey, true); err != nil {
		t.Fatal(err)
	}
	if st, _ := CheckHostKey(kh, "keep.example", 22, keep); st != TrustTrusted {
		t.Errorf("keep.example was disturbed by replacing target.example: %v", st)
	}
}

func TestHostKeyError_Message(t *testing.T) {
	unknown := &HostKeyError{Host: "h", Port: 2222}
	if got := unknown.Error(); !strings.Contains(got, "iceclimber trust") || !strings.Contains(got, "unknown host") {
		t.Errorf("unknown msg = %q", got)
	}
	changed := &HostKeyError{Host: "h", Port: 2222, Mismatch: true}
	if got := changed.Error(); !strings.Contains(got, "--replace") || !strings.Contains(got, "CHANGED") {
		t.Errorf("changed msg = %q", got)
	}
}
