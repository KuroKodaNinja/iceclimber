// Package remotefstest provides a conformance suite that asserts any
// remotefs.FS implementation behaves identically. It is run from fast local unit
// tests (ExecFS over a local shell, SFTPFS over an in-process pipe) and from the
// functional suite against a real VM over both SSH channels.
package remotefstest

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// NewFS returns a fresh FS and an existing, empty base directory to work under.
// It is called once per subtest so cases never interfere.
type NewFS func(t *testing.T) (rfs remotefs.FS, base string)

// RunConformance exercises the behavioral contract every FS must honor.
func RunConformance(t *testing.T, newFS NewFS) {
	ctx := context.Background()

	t.Run("write_read_roundtrip", func(t *testing.T) {
		rfs, base := newFS(t)
		p := base + "/file.bin"
		data := []byte("hello\x00binary\nworld") // includes a NUL and a newline
		mustOK(t, rfs.WriteFile(ctx, p, data))
		got, err := rfs.ReadFile(ctx, p)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Errorf("roundtrip mismatch: got %q, want %q", got, data)
		}
	})

	t.Run("list_empty_dir_is_not_error", func(t *testing.T) {
		rfs, base := newFS(t)
		d := base + "/empty"
		mustOK(t, rfs.Mkdir(ctx, d))
		names, err := rfs.List(ctx, d)
		if err != nil {
			t.Fatalf("List of empty dir errored: %v", err)
		}
		if len(names) != 0 {
			t.Errorf("List of empty dir = %v, want []", names)
		}
	})

	t.Run("mkdir_p_nested_then_list_sorted", func(t *testing.T) {
		rfs, base := newFS(t)
		d := base + "/a/b/c" // exercises mkdir -p / MkdirAll
		mustOK(t, rfs.Mkdir(ctx, d))
		mustOK(t, rfs.WriteFile(ctx, d+"/y", []byte("2")))
		mustOK(t, rfs.WriteFile(ctx, d+"/x", []byte("1")))
		names, err := rfs.List(ctx, d)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(names) != 2 || names[0] != "x" || names[1] != "y" {
			t.Errorf("List = %v, want [x y] (sorted basenames)", names)
		}
	})

	t.Run("rename_replaces_target_and_removes_source", func(t *testing.T) {
		rfs, base := newFS(t)
		src, dst := base+"/src", base+"/dst"
		mustOK(t, rfs.WriteFile(ctx, src, []byte("new")))
		mustOK(t, rfs.WriteFile(ctx, dst, []byte("old"))) // dst exists -> must be replaced
		mustOK(t, rfs.Rename(ctx, src, dst))
		got, err := rfs.ReadFile(ctx, dst)
		if err != nil {
			t.Fatalf("ReadFile after rename: %v", err)
		}
		if string(got) != "new" {
			t.Errorf("dst = %q after rename, want %q (replace semantics)", got, "new")
		}
		if _, err := rfs.ReadFile(ctx, src); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("source still readable after rename; err = %v, want ErrNotExist", err)
		}
	})

	t.Run("read_missing_is_ErrNotExist", func(t *testing.T) {
		rfs, base := newFS(t)
		if _, err := rfs.ReadFile(ctx, base+"/nope"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ReadFile missing: err = %v, want ErrNotExist", err)
		}
	})

	t.Run("list_missing_is_ErrNotExist", func(t *testing.T) {
		rfs, base := newFS(t)
		if _, err := rfs.List(ctx, base+"/nope"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("List missing: err = %v, want ErrNotExist", err)
		}
	})

	t.Run("rename_missing_source_is_ErrNotExist", func(t *testing.T) {
		rfs, base := newFS(t)
		if err := rfs.Rename(ctx, base+"/nope", base+"/dst"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Rename missing source: err = %v, want ErrNotExist", err)
		}
	})

	t.Run("chmod_existing_and_missing", func(t *testing.T) {
		rfs, base := newFS(t)
		p := base + "/f"
		mustOK(t, rfs.WriteFile(ctx, p, []byte("x")))
		mustOK(t, rfs.Chmod(ctx, p, 0o755))
		if err := rfs.Chmod(ctx, base+"/nope", 0o755); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Chmod missing: err = %v, want ErrNotExist", err)
		}
	})

	t.Run("symlink_reads_through_to_target", func(t *testing.T) {
		rfs, base := newFS(t)
		target, link := base+"/target", base+"/link"
		mustOK(t, rfs.WriteFile(ctx, target, []byte("payload")))
		mustOK(t, rfs.Symlink(ctx, target, link))
		got, err := rfs.ReadFile(ctx, link)
		if err != nil {
			t.Fatalf("ReadFile through symlink: %v", err)
		}
		if string(got) != "payload" {
			t.Errorf("through-link content = %q, want payload", got)
		}
	})

	t.Run("symlink_is_idempotent", func(t *testing.T) {
		// Re-creating an existing link must not fail (plain SFTP SSH_FXP_SYMLINK returns
		// SSH_FX_FAILURE on an existing name) — a re-relay or two packages contributing the
		// same bin link would otherwise abort. The second Symlink also repoints the link.
		rfs, base := newFS(t)
		a, b, link := base+"/a", base+"/b", base+"/link"
		mustOK(t, rfs.WriteFile(ctx, a, []byte("A")))
		mustOK(t, rfs.WriteFile(ctx, b, []byte("B")))
		mustOK(t, rfs.Symlink(ctx, a, link))
		mustOK(t, rfs.Symlink(ctx, b, link)) // was SSH_FX_FAILURE before the idempotency fix
		got, err := rfs.ReadFile(ctx, link)
		if err != nil {
			t.Fatalf("ReadFile through re-pointed symlink: %v", err)
		}
		if string(got) != "B" {
			t.Errorf("re-created link content = %q, want B (repointed)", got)
		}
	})

	t.Run("removeall_file_dir_and_missing", func(t *testing.T) {
		rfs, base := newFS(t)
		// A file.
		f := base + "/f"
		mustOK(t, rfs.WriteFile(ctx, f, []byte("x")))
		mustOK(t, rfs.RemoveAll(ctx, f))
		if _, err := rfs.ReadFile(ctx, f); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("file present after RemoveAll: %v", err)
		}
		// A directory with contents (recursive).
		d := base + "/d"
		mustOK(t, rfs.Mkdir(ctx, d))
		mustOK(t, rfs.WriteFile(ctx, d+"/inner", []byte("y")))
		mustOK(t, rfs.RemoveAll(ctx, d))
		if _, err := rfs.List(ctx, d); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("dir present after RemoveAll: %v", err)
		}
		// Missing path is idempotent — no error.
		if err := rfs.RemoveAll(ctx, base+"/nope"); err != nil {
			t.Errorf("RemoveAll of a missing path = %v, want nil", err)
		}
	})
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
