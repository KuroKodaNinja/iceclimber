package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	iofs "io/fs"
	"path"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/wire"
)

// Tree / Maildir layout + id helpers re-exported from the wire leaf package.
type (
	Tree    = wire.Tree
	Maildir = wire.Maildir
)

var (
	NewID       = wire.NewID
	RequestName = wire.RequestName
)

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

// IsBootstrapped reports whether the sandbox at t has been provisioned. It checks for
// skill/NANA.md — written unconditionally by bootstrap's provision and never by serve — so it
// distinguishes a real iceclimber sandbox from a bare/fresh remote_root. A missing file is a
// clean false (not an error); a transport error propagates so callers don't misread an
// unreachable box as unprovisioned.
func IsBootstrapped(ctx context.Context, fs remotefs.FS, t Tree) (bool, error) {
	_, err := fs.ReadFile(ctx, t.SkillFile())
	if err == nil {
		return true, nil
	}
	if errors.Is(err, iofs.ErrNotExist) {
		return false, nil
	}
	return false, err
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

// AckResponse marks a response collected by moving it inbox/new -> inbox/cur, so the GC
// can prune the request/response pair and inbox/new reflects only uncollected mail. The
// in-sandbox agent does this (popo, on read); this is the controller-side analogue for a
// controller that reads a response (e.g. the bootstrap smoke test).
func AckResponse(ctx context.Context, fs remotefs.FS, t Tree, name string) error {
	return fs.Rename(ctx, path.Join(t.Inbox().New(), name), path.Join(t.Inbox().Cur(), name))
}

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
