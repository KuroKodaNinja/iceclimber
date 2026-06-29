package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/oklog/ulid/v2"
)

// Tree is the on-sandbox layout rooted at an install root (plan §3). All paths
// are absolute POSIX paths (path, not path/filepath).
type Tree struct {
	Root string
}

func (t Tree) protocolDir() string { return path.Join(t.Root, "protocol") }

// Outbox carries requests (Nana -> Popo); Inbox carries responses (Popo -> Nana).
func (t Tree) Outbox() Maildir { return Maildir{base: path.Join(t.protocolDir(), "outbox")} }
func (t Tree) Inbox() Maildir  { return Maildir{base: path.Join(t.protocolDir(), "inbox")} }

// Heartbeat is the liveness file Popo writes (plan §4.7).
func (t Tree) Heartbeat() string { return path.Join(t.protocolDir(), "heartbeat") }

// Blobs is the content-addressed store; State holds convenience copies.
func (t Tree) Blobs() string { return path.Join(t.protocolDir(), "blobs") }
func (t Tree) State() string { return path.Join(t.Root, "state") }

// BlobRef is the $ROOT-relative path of a blob, as published in a response's
// body_blob field — the agent reads it at $ROOT/<BlobRef>. Derived from Blobs() so
// the published reference can never drift from where blobs are actually written.
func (t Tree) BlobRef(name string) string {
	return strings.TrimPrefix(path.Join(t.Blobs(), name), t.Root+"/")
}

// Skill holds the dropped NANA.md skill doc; Capabilities is Nana's self-report.
func (t Tree) Skill() string        { return path.Join(t.Root, "skill") }
func (t Tree) SkillFile() string    { return path.Join(t.Skill(), "NANA.md") }
func (t Tree) Capabilities() string { return path.Join(t.protocolDir(), "capabilities.json") }

// Maildir is one tmp/new/cur triple.
type Maildir struct{ base string }

func (m Maildir) Tmp() string { return path.Join(m.base, "tmp") }
func (m Maildir) New() string { return path.Join(m.base, "new") }
func (m Maildir) Cur() string { return path.Join(m.base, "cur") }

// EnsureTree creates every directory the protocol needs (idempotent — mkdir -p).
func EnsureTree(ctx context.Context, fs remotefs.FS, t Tree) error {
	for _, d := range []string{
		t.Outbox().Tmp(), t.Outbox().New(), t.Outbox().Cur(),
		t.Inbox().Tmp(), t.Inbox().New(), t.Inbox().Cur(),
		t.Blobs(), t.State(), t.Skill(),
	} {
		if err := fs.Mkdir(ctx, d); err != nil {
			return fmt.Errorf("ensure tree %s: %w", d, err)
		}
	}
	return nil
}

// Deliver atomically publishes data into the maildir: write to tmp/, then rename
// into new/. Readers of new/ therefore never observe a partial file.
func Deliver(ctx context.Context, fs remotefs.FS, m Maildir, name string, data []byte) error {
	if err := fs.WriteFile(ctx, path.Join(m.Tmp(), name), data); err != nil {
		return fmt.Errorf("deliver write tmp: %w", err)
	}
	if err := fs.Rename(ctx, path.Join(m.Tmp(), name), path.Join(m.New(), name)); err != nil {
		return fmt.Errorf("deliver publish: %w", err)
	}
	return nil
}

// PickUp moves name from new/ to cur/ — the rename is the pickup lock (plan §3).
func PickUp(ctx context.Context, fs remotefs.FS, m Maildir, name string) error {
	return fs.Rename(ctx, path.Join(m.New(), name), path.Join(m.Cur(), name))
}

// NewID returns a fresh ULID. ULIDs sort lexically by creation time, so "oldest
// queued" is a plain directory listing (plan §3).
func NewID() string { return ulid.Make().String() }

// RequestName is the filename for a request/response with the given id.
func RequestName(id string) string { return id + ".json" }

// ReadResponse reads and parses a response by filename from inbox/new.
func ReadResponse(ctx context.Context, fs remotefs.FS, t Tree, name string) (*Response, error) {
	data, err := fs.ReadFile(ctx, path.Join(t.Inbox().New(), name))
	if err != nil {
		return nil, err
	}
	var r Response
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse response %s: %w", name, err)
	}
	return &r, nil
}
