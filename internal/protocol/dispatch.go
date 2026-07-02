package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

// Dispatcher services requests for one sandbox tree over a remotefs.FS. It is the
// engine behind both `serve` and `bootstrap`'s smoke test.
type Dispatcher struct {
	fs           remotefs.FS
	tree         Tree
	registry     Registry
	seq          int64 // written only by the heartbeat goroutine (sole writer)
	observe      func(ServiceEvent)
	observeStart func(StartEvent)
	gate         func(context.Context, Request) error
	onHeartbeat  func(seq int64)
	retention    time.Duration // reap responses uncollected this long (0 = never)
	concurrency  int           // max handlers serviced at once (<1 ⇒ 1, i.e. sequential)
}

// CodeOperatorDenied is the error code on a response the gate rejected before the
// handler ran. Such a request was denied, not serviced — observers exclude it from
// the "serviced" tally (the approver logs it as a denial).
const CodeOperatorDenied = "operator_denied"

// ServiceEvent reports one serviced request to an optional observer. It carries
// primitives only, so the dispatcher stays decoupled from logging (the cli layer
// turns this into an activity-log event).
type ServiceEvent struct {
	Name string // the request file name (<id>.json)
	Req  Request
	Resp Response
	Dur  time.Duration
}

// StartEvent reports a request being picked up — fired the moment the dispatcher
// reads it from cur/, before the handler (or gate) runs. It lets an observer show a
// request in-progress 1:1, rather than only after completion (which ServiceEvent
// reports). It carries the request file name and the parsed Request; like
// ServiceEvent it stays free of logging coupling (the cli layer turns it into an
// activity event).
type StartEvent struct {
	Name string // the request file name (<id>.json)
	Req  Request
}

// NewDispatcher builds a dispatcher.
func NewDispatcher(fs remotefs.FS, tree Tree, reg Registry) *Dispatcher {
	return &Dispatcher{fs: fs, tree: tree, registry: reg}
}

// Observe registers a callback invoked once per serviced request (after the
// response is delivered). Used by `serve` to feed the activity log. Optional —
// a nil observer is silent.
func (d *Dispatcher) Observe(fn func(ServiceEvent)) { d.observe = fn }

// ObserveStart registers a callback invoked when a request is picked up, before its
// handler runs (the sibling of Observe). Fires for every serviced request — from both
// drainNew and recover, agent- and operator-initiated alike — so the console can show
// it in-progress. Optional; a nil callback is silent.
func (d *Dispatcher) ObserveStart(fn func(StartEvent)) { d.observeStart = fn }

// SetGate registers a pre-execution gate consulted just before a request's
// handler runs. A non-nil error short-circuits the request to an
// "operator_denied" response (the handler never runs). Used by interactive
// `serve` to prompt the operator. Optional — a nil gate runs everything.
func (d *Dispatcher) SetGate(fn func(context.Context, Request) error) { d.gate = fn }

// OnHeartbeat registers a callback fired after each heartbeat write (with the new
// seq). The console uses it to show a live "serving / stale" indicator. Optional.
func (d *Dispatcher) OnHeartbeat(fn func(seq int64)) { d.onHeartbeat = fn }

// SetRetention sets how long a delivered-but-uncollected response may sit in
// inbox/new before GC reaps it (with its request). 0 (the default) disables the
// retention sweep — collected pairs are still pruned. The clock is the response's
// CompletedAt (delivery time), so a long install never trips it.
func (d *Dispatcher) SetRetention(dur time.Duration) { d.retention = dur }

// SetConcurrency sets how many requests may be serviced at once (the bounded worker pool).
// A value < 1 means 1 (fully sequential — the pre-concurrency behavior, and what bootstrap's
// smoke test uses). See PROTOCOL.md "Concurrency" for the contract this enforces.
func (d *Dispatcher) SetConcurrency(n int) { d.concurrency = n }

// workers is the effective pool size (never < 1).
func (d *Dispatcher) workers() int {
	if d.concurrency < 1 {
		return 1
	}
	return d.concurrency
}

// RunOnce performs a single dispatch cycle: GC completed/abandoned pairs, recover any
// in-flight requests left in cur/ by a previous crash, then drain new/ oldest-first.
// GC runs first so a reaped abandoned request isn't wastefully re-serviced by recover.
// Used by `serve --once` and the bootstrap smoke test.
func (d *Dispatcher) RunOnce(ctx context.Context) error {
	if err := d.gc(ctx); err != nil {
		return err
	}
	if err := d.recover(ctx); err != nil {
		return err
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	dr := newDrainer(d.workers(), workCtx, cancel)
	// Re-drain until a pass submits nothing new: a same-key request skipped this pass (its key
	// busy) is serviced on the next, once the holder finishes — Serve gets this from its ticker,
	// RunOnce has none, so it loops. Stays fully synchronous to the caller (bootstrap smoke test
	// and `serve --once` expect all work done on return).
	for {
		n, err := d.drainNew(ctx, dr)
		if err != nil {
			cancel()
			dr.wg.Wait()
			return dr.errOr(err)
		}
		if n == 0 {
			break
		}
		dr.wg.Wait() // finish this batch so freed keys unblock the next pass
	}
	dr.wg.Wait()
	return dr.errOr(nil)
}

// Serve drains the outbox in a loop, with the heartbeat on a SEPARATE goroutine so a
// long-running handler (a big install) or a blocked approval gate can't starve it —
// nana judges Popo alive on seq advancement, and a stalled heartbeat would otherwise
// read as a dead Popo (and mislead the console's serving indicator). It recovers once
// at startup and returns when ctx is cancelled (or the drain hits a transport error,
// which the serve supervisor catches to reconnect).
func (d *Dispatcher) Serve(ctx context.Context, interval time.Duration) error {
	if err := d.recover(ctx); err != nil {
		return err
	}
	// Heartbeat ticker, scoped to this Serve call (stopped on return). The fs write
	// rides its own SSH channel (sftp/ssh sessions are concurrency-safe), so it keeps
	// advancing while drainNew services a request.
	hbCtx, hbStop := context.WithCancel(ctx)
	defer hbStop()
	go d.heartbeatLoop(hbCtx, interval)

	// Bounded worker pool for this Serve session (shared across cycles). workCtx is cancelled
	// on teardown OR the first worker transport error; the deferred wg.Wait drains in-flight
	// handlers on every exit path so no response is left half-written and no goroutine leaks.
	workCtx, cancel := context.WithCancel(ctx)
	dr := newDrainer(d.workers(), workCtx, cancel)
	defer func() { cancel(); dr.wg.Wait() }()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		// A worker transport error (or teardown) cancels workCtx — stop before re-draining, and
		// surface the real worker error (errOr) rather than a masked context.Canceled.
		if err := workCtx.Err(); err != nil {
			return dr.errOr(err)
		}
		// gc/drain use the parent ctx for their own fs ops (a worker-triggered workCtx cancel
		// must not mask the real worker error with a context.Canceled here — dr.errOr recovers it).
		if err := d.gc(ctx); err != nil {
			return dr.errOr(err)
		}
		if _, err := d.drainNew(ctx, dr); err != nil {
			return dr.errOr(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-dr.errCh:
			return err
		case <-ticker.C:
		}
	}
}

// heartbeatLoop is the sole writer of the heartbeat (and of d.seq). It writes one
// immediately (liveness visible without waiting a full interval), then every interval
// until ctx is cancelled. A failed write (e.g. the link dropped) is non-fatal — the
// next tick retries, and the drain loop's own error drives reconnect; meanwhile the
// console's freshness indicator shows the heartbeat going stale.
func (d *Dispatcher) heartbeatLoop(ctx context.Context, interval time.Duration) {
	_ = d.WriteHeartbeat(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = d.WriteHeartbeat(ctx)
		}
	}
}

// WriteHeartbeat bumps the sequence and atomically replaces the heartbeat file
// with "<seq> <iso8601>". Nana judges liveness on seq advancement, which needs
// no clock sync (plan §4.7, decision #8). Called only from heartbeatLoop (Serve) and
// once by the bootstrap smoke test, so d.seq has a single writer at a time.
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
	if d.onHeartbeat != nil {
		d.onHeartbeat(d.seq)
	}
	return nil
}

// drainNew picks up every serviceable request in outbox/new (oldest first — List returns ULID
// names in creation order) and submits its handler to the bounded worker pool. Pickup (dedup +
// the new→cur rename) stays SERIAL and fast on the drain goroutine; the handler runs async on a
// worker, so a long install no longer blocks the queue behind it. Returns the number of handlers
// submitted (RunOnce loops until 0) and a transport error from a pickup-path op (drives
// reconnect); per-request handler errors surface via dr.errCh, not here.
//
// Two in-memory guards keep concurrency safe (PROTOCOL.md "Concurrency"):
//   - inflight (by request name): a request re-delivered while its handler still runs is not
//     picked up again — else the PosixRename would clobber the live cur/ file and a second worker
//     would double-service it (responseExists can't see an in-flight request; it lists inbox).
//   - active keys (by resourceKey): at most one in-flight handler per shared target (a runtime
//     line / env), so two installs can't corrupt one environment; distinct keys run in parallel.
//     A key-busy request is left in new/ and retried a later cycle.
//
// fs ops here use the parent ctx (not workCtx) so a worker-triggered cancel can't abort the
// drain mid-op and mask the real error; the sem acquire watches workCtx so a full pool still
// unblocks promptly on teardown.
func (d *Dispatcher) drainNew(ctx context.Context, dr *drainer) (int, error) {
	names, err := d.fs.List(ctx, d.tree.Outbox().New())
	if err != nil {
		return 0, fmt.Errorf("list outbox/new: %w", err)
	}
	submitted := 0
	for _, name := range names {
		if !validRequestName(name) || dr.inflightHas(name) {
			continue
		}
		done, err := d.responseExists(ctx, name)
		if err != nil {
			return submitted, err
		}
		if done {
			// Already answered — clear it from new/ (a re-delivery of a completed request).
			if err := PickUp(ctx, d.fs, d.tree.Outbox(), name); err != nil {
				return submitted, fmt.Errorf("pickup %s: %w", name, err)
			}
			continue
		}
		data, err := d.fs.ReadFile(ctx, path.Join(d.tree.Outbox().New(), name))
		if err != nil {
			return submitted, fmt.Errorf("read %s: %w", name, err)
		}
		key := resourceKey(data)
		if !dr.reserve(name, key) {
			continue // key busy — leave in new/, retry a later cycle
		}
		// Backpressure: wait for a pool slot, but honor teardown (a worker error cancels workCtx).
		select {
		case dr.sem <- struct{}{}:
		case <-dr.done:
			dr.release(name, key)
			return submitted, nil
		}
		if err := PickUp(ctx, d.fs, d.tree.Outbox(), name); err != nil {
			<-dr.sem
			dr.release(name, key)
			return submitted, fmt.Errorf("pickup %s: %w", name, err)
		}
		submitted++
		dr.wg.Add(1)
		go func(name, key string, data []byte) {
			defer dr.wg.Done()
			defer func() { <-dr.sem }()
			defer dr.release(name, key)
			if err := d.serviceCurData(dr.ctx, name, data); err != nil {
				dr.fail(err) // transport error → errCh + cancel workCtx
			}
		}(name, key, data)
	}
	return submitted, nil
}

// drainer is the per-Serve/RunOnce bounded worker pool + the in-flight/active-key guards. It is
// created fresh each invocation (never on the Dispatcher, which is rebuilt every reconnect and
// whose d.seq must keep its single-writer invariant).
type drainer struct {
	ctx    context.Context // workCtx — handlers run under it (cancelled on teardown/error)
	done   <-chan struct{} // workCtx.Done() — the sem acquire watches it
	sem    chan struct{}   // buffered, size N — the concurrency cap
	wg     sync.WaitGroup  // in-flight workers (awaited on teardown)
	errCh  chan error      // buffered 1 — first worker transport error wins
	cancel context.CancelFunc

	mu       sync.Mutex
	inflight map[string]struct{} // request names currently owned by a worker
	active   map[string]struct{} // resource keys currently serviced (serialize same-target)
}

func newDrainer(n int, workCtx context.Context, cancel context.CancelFunc) *drainer {
	return &drainer{
		ctx: workCtx, done: workCtx.Done(),
		sem: make(chan struct{}, n), errCh: make(chan error, 1), cancel: cancel,
		inflight: map[string]struct{}{}, active: map[string]struct{}{},
	}
}

func (dr *drainer) inflightHas(name string) bool {
	dr.mu.Lock()
	defer dr.mu.Unlock()
	_, ok := dr.inflight[name]
	return ok
}

// reserve marks name in-flight and, if keyed, locks its resource key. Returns false (no
// reservation) when the key is already active — the caller leaves the request queued.
func (dr *drainer) reserve(name, key string) bool {
	dr.mu.Lock()
	defer dr.mu.Unlock()
	if key != "" {
		if _, busy := dr.active[key]; busy {
			return false
		}
		dr.active[key] = struct{}{}
	}
	dr.inflight[name] = struct{}{}
	return true
}

func (dr *drainer) release(name, key string) {
	dr.mu.Lock()
	defer dr.mu.Unlock()
	delete(dr.inflight, name)
	if key != "" {
		delete(dr.active, key)
	}
}

// fail records the first worker transport error and cancels workCtx so a wedged drain unblocks
// and Serve returns it (→ reconnect). Extra errors are dropped (errCh is buffered 1).
func (dr *drainer) fail(err error) {
	select {
	case dr.errCh <- err:
	default:
	}
	dr.cancel()
}

// errOr returns a pending worker error if one occurred, else the passed-in error — so a
// context.Canceled produced by a worker-triggered cancel doesn't mask the real cause.
func (dr *drainer) errOr(err error) error {
	select {
	case werr := <-dr.errCh:
		return werr
	default:
		return err
	}
}

// resourceKey groups requests that touch the same runtime/environment so Popo serializes them
// (PROTOCOL.md "Concurrency"): distinct keys run in parallel, same key runs one at a time. An
// empty key means no shared target (ping/collect/web.fetch) — always parallel. The family is the
// contract surface (asserted against PROTOCOL.md); the value narrows it to a specific target
// (version or project/env) where the params make that cheap and safe.
func resourceKey(data []byte) string {
	var req struct {
		Type   string `json:"type"`
		Params struct {
			Version       string `json:"version"`
			PythonVersion string `json:"python_version"`
			NodeVersion   string `json:"node_version"`
			JavaVersion   string `json:"java_version"`
			Project       string `json:"project"`
			File          string `json:"file"`
		} `json:"params"`
	}
	_ = json.Unmarshal(data, &req)
	fam := runtimeFamily(req.Type)
	if fam == "" {
		return ""
	}
	p := req.Params
	// Prefer the most specific target so distinct projects/envs parallelize; fall back to the
	// runtime version, then the bare family (safe over-serialization if params don't parse).
	target := firstNonEmpty(p.Project, p.File, p.Version, p.PythonVersion, p.NodeVersion, p.JavaVersion)
	return fam + ":" + target
}

// runtimeFamily maps a verb to the runtime line it mutates (the same grouping PROTOCOL.md
// documents). "" = no shared target. Kept as the single source of truth the doc is tested against.
func runtimeFamily(reqType string) string {
	switch reqType {
	case "python.install", "pip.install":
		return "python"
	case "node.install", "npm.install":
		return "node"
	case "java.install", "maven.install", "maven.build":
		return "java"
	case "conda.install":
		return "conda"
	default:
		return ""
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
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

// gc prunes the maildir of completed and abandoned request/response pairs — keeping the
// "uncollected" count (inbox/new) honest and disk bounded. Runs first each cycle.
//
// Two passes, both keyed by the shared <id>.json basename:
//   - Collected: a response the agent has read is moved to inbox/cur (popo renames
//     new->cur on read). Delete the coupled pair outbox/cur/<id> + inbox/cur/<id>.
//   - Retention (when d.retention > 0): a response delivered to inbox/new but uncollected
//     for longer than d.retention — measured from its CompletedAt (delivery time, so a
//     long install never trips it) — is abandoned; delete inbox/new/<id> + outbox/cur/<id>.
//
// Safe: a GC'd ULID is never re-delivered (unique ids; a retry uses a new id), so
// dedup/recover never re-execute a reaped pair; and a response reaches inbox/cur only
// AFTER the agent fully read it, so GC never races a reader. Best-effort — a listing
// hiccup is swallowed (the drainNew that follows surfaces a real transport drop), and
// RemoveAll is idempotent so a missing mate is fine.
func (d *Dispatcher) gc(ctx context.Context) error {
	// Collected pairs.
	if collected, err := d.fs.List(ctx, d.tree.Inbox().Cur()); err == nil {
		for _, name := range collected {
			if !validRequestName(name) {
				continue // never let a crafted name steer the destructive RemoveAll
			}
			_ = d.fs.RemoveAll(ctx, path.Join(d.tree.Outbox().Cur(), name))
			_ = d.fs.RemoveAll(ctx, path.Join(d.tree.Inbox().Cur(), name))
		}
	}
	// Abandoned pairs: delivered but uncollected past the retention window.
	if d.retention <= 0 {
		return nil
	}
	uncollected, err := d.fs.List(ctx, d.tree.Inbox().New())
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-d.retention)
	for _, name := range uncollected {
		if !validRequestName(name) {
			continue
		}
		data, rerr := d.fs.ReadFile(ctx, path.Join(d.tree.Inbox().New(), name))
		if rerr != nil {
			continue
		}
		var r Response
		if json.Unmarshal(data, &r) != nil || r.CompletedAt.IsZero() || r.CompletedAt.After(cutoff) {
			continue // unparseable/undated/recent — leave it (the agent may still collect)
		}
		_ = d.fs.RemoveAll(ctx, path.Join(d.tree.Inbox().New(), name))
		_ = d.fs.RemoveAll(ctx, path.Join(d.tree.Outbox().Cur(), name))
	}
	return nil
}

// validRequestName reports whether name is a safe maildir entry to act on: a
// "<id>.json" basename, never "."/".."/a path with separators. gc guards its
// destructive RemoveAll with this — on the exec transport a filename containing a
// newline splits in `ls -1` output (List) into a ".." fragment, which would otherwise
// steer RemoveAll one directory up. (SFTP returns clean basenames; this is defense in
// depth for both.)
func validRequestName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		name == path.Base(name) && !strings.ContainsRune(name, '/') &&
		strings.HasSuffix(name, ".json")
}

// serviceCur reads the request from cur/ then services it — used by recover (crash recovery),
// which runs sequentially at startup. drainNew's workers call serviceCurData directly with the
// data already read from new/ (avoiding a re-read after pickup).
func (d *Dispatcher) serviceCur(ctx context.Context, name string) error {
	data, err := d.fs.ReadFile(ctx, path.Join(d.tree.Outbox().Cur(), name))
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	return d.serviceCurData(ctx, name, data)
}

// serviceCurData dispatches an already-read request and delivers the response to the inbox.
// Safe to run concurrently across distinct requests: it touches only per-request files
// (cur/<name> was already picked up; the response is delivered atomically to inbox/<name>) and
// the observers push to a non-blocking channel. A returned error is a TRANSPORT failure
// (marshal/deliver) — the caller treats it as a dropped link (reconnect); handler errors become
// error Responses and are delivered normally.
func (d *Dispatcher) serviceCurData(ctx context.Context, name string, data []byte) error {
	if d.observeStart != nil {
		var req Request // best-effort: the request may not parse, but we still mark pickup
		_ = json.Unmarshal(data, &req)
		d.observeStart(StartEvent{Name: name, Req: req})
	}
	start := time.Now()
	resp := d.dispatch(ctx, name, data)
	if resp.CompletedAt.IsZero() {
		resp.CompletedAt = time.Now().UTC() // GC retention clock; OK/Errf set this, raw handlers may not
	}
	respBytes, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal response %s: %w", name, err)
	}
	if err := Deliver(ctx, d.fs, d.tree.Inbox(), name, respBytes); err != nil {
		return fmt.Errorf("deliver response %s: %w", name, err)
	}
	if d.observe != nil {
		var req Request // best-effort: resp carries id/status even if this fails
		_ = json.Unmarshal(data, &req)
		d.observe(ServiceEvent{Name: name, Req: req, Resp: resp, Dur: time.Since(start)})
	}
	return nil
}

// dispatch parses a request and routes it to its handler. A handler panic is recovered into an
// error Response (not a crash) — workers are now detached goroutines, so one bad handler must
// not take down the whole serve daemon.
func (d *Dispatcher) dispatch(ctx context.Context, name string, data []byte) (resp Response) {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return Errf(idFromName(name), "malformed_request", "parse request: %v", err)
	}
	h, ok := d.registry[req.Type]
	if !ok {
		return Errf(req.ID, "unknown_type", "no handler for request type %q", req.Type)
	}
	if d.gate != nil {
		if err := d.gate(ctx, req); err != nil {
			return Errf(req.ID, CodeOperatorDenied, "%v", err)
		}
	}
	defer func() {
		if r := recover(); r != nil {
			resp = Errf(req.ID, "handler_panic", "handler panicked: %v", r)
		}
	}()
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
