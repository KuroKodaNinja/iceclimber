package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Dispatcher services requests for one sandbox tree over a remotefs.FS. It is the
// engine behind both `serve` and `bootstrap`'s smoke test.
type Dispatcher struct {
	fs       remotefs.FS
	tree     Tree
	registry Registry
	seq      int64
}

// NewDispatcher builds a dispatcher.
func NewDispatcher(fs remotefs.FS, tree Tree, reg Registry) *Dispatcher {
	return &Dispatcher{fs: fs, tree: tree, registry: reg}
}

// RunOnce performs a single dispatch cycle: recover any in-flight requests left
// in cur/ by a previous crash, then drain new/ oldest-first. Used by
// `serve --once` and the bootstrap smoke test.
func (d *Dispatcher) RunOnce(ctx context.Context) error {
	if err := d.recover(ctx); err != nil {
		return err
	}
	return d.drainNew(ctx)
}

// Serve loops: write the heartbeat, drain the outbox, sleep. It recovers once at
// startup. Returns when ctx is cancelled.
func (d *Dispatcher) Serve(ctx context.Context, interval time.Duration) error {
	if err := d.recover(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := d.WriteHeartbeat(ctx); err != nil {
			return err
		}
		if err := d.drainNew(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// WriteHeartbeat bumps the sequence and atomically replaces the heartbeat file
// with "<seq> <iso8601>". Nana judges liveness on seq advancement, which needs
// no clock sync (plan §4.7, decision #8).
func (d *Dispatcher) WriteHeartbeat(ctx context.Context) error {
	d.seq++
	content := strconv.FormatInt(d.seq, 10) + " " + time.Now().UTC().Format(time.RFC3339) + "\n"
	tmp := d.tree.Heartbeat() + ".tmp"
	if err := d.fs.WriteFile(ctx, tmp, []byte(content)); err != nil {
		return fmt.Errorf("heartbeat write: %w", err)
	}
	if err := d.fs.Rename(ctx, tmp, d.tree.Heartbeat()); err != nil {
		return fmt.Errorf("heartbeat publish: %w", err)
	}
	return nil
}

// drainNew services every request currently in outbox/new, oldest first (List
// returns ULID names sorted = creation order).
func (d *Dispatcher) drainNew(ctx context.Context) error {
	names, err := d.fs.List(ctx, d.tree.Outbox().New())
	if err != nil {
		return fmt.Errorf("list outbox/new: %w", err)
	}
	for _, name := range names {
		if err := d.serviceFromNew(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// recover re-services requests stranded in cur/ (picked up but never answered,
// e.g. a crash mid-cycle). All phase-2 handlers are idempotent, so re-dispatch
// is safe; non-idempotent verbs will fail-closed here in a later phase (§4).
func (d *Dispatcher) recover(ctx context.Context) error {
	names, err := d.fs.List(ctx, d.tree.Outbox().Cur())
	if err != nil {
		return fmt.Errorf("list outbox/cur: %w", err)
	}
	for _, name := range names {
		done, err := d.responseExists(ctx, name)
		if err != nil {
			return err
		}
		if done {
			continue
		}
		if err := d.serviceCur(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// serviceFromNew dedups, picks up (new->cur), then services a request.
func (d *Dispatcher) serviceFromNew(ctx context.Context, name string) error {
	done, err := d.responseExists(ctx, name)
	if err != nil {
		return err
	}
	if err := PickUp(ctx, d.fs, d.tree.Outbox(), name); err != nil {
		return fmt.Errorf("pickup %s: %w", name, err)
	}
	if done {
		return nil // already answered; just cleared from new/
	}
	return d.serviceCur(ctx, name)
}

// serviceCur reads the request from cur/, dispatches it, and delivers the
// response to the inbox.
func (d *Dispatcher) serviceCur(ctx context.Context, name string) error {
	data, err := d.fs.ReadFile(ctx, path.Join(d.tree.Outbox().Cur(), name))
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	resp := d.dispatch(ctx, name, data)
	respBytes, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal response %s: %w", name, err)
	}
	if err := Deliver(ctx, d.fs, d.tree.Inbox(), name, respBytes); err != nil {
		return fmt.Errorf("deliver response %s: %w", name, err)
	}
	return nil
}

// dispatch parses a request and routes it to its handler.
func (d *Dispatcher) dispatch(ctx context.Context, name string, data []byte) Response {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return Errf(idFromName(name), "malformed_request", "parse request: %v", err)
	}
	h, ok := d.registry[req.Type]
	if !ok {
		return Errf(req.ID, "unknown_type", "no handler for request type %q", req.Type)
	}
	return h(ctx, req)
}

// responseExists reports whether a response for name already exists in the inbox
// (new or cur) — the basis for effectively-once delivery (plan §4, decision #13).
func (d *Dispatcher) responseExists(ctx context.Context, name string) (bool, error) {
	for _, dir := range []string{d.tree.Inbox().New(), d.tree.Inbox().Cur()} {
		names, err := d.fs.List(ctx, dir)
		if err != nil {
			return false, fmt.Errorf("list %s: %w", dir, err)
		}
		for _, n := range names {
			if n == name {
				return true, nil
			}
		}
	}
	return false, nil
}

func idFromName(name string) string { return strings.TrimSuffix(name, ".json") }
