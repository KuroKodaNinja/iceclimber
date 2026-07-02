// Package activity writes the controller-side, append-only activity log: a
// per-sandbox JSONL stream of request-lifecycle and operator events, so an
// operator can tail what Popo is doing (see `iceclimber logs`). It is the
// host-side companion to the agent's own output stream, and — like the audit log
// (internal/audit) — Popo's copy is authoritative and lives on the controller.
package activity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/logfile"
)

// Event kinds.
const (
	KindServiced = "serviced" // Popo serviced a request (id/type/status/dur)
	KindApproved = "approved" // operator approved a held egress
	KindDenied   = "denied"   // operator denied a held egress
	KindOperated = "operated" // operator-initiated action (console install/bootstrap)
	KindVerified = "verified" // sandbox-side confirmation (Nana echo: ran in the sandbox)
	// KindStarted marks a request picked up and in progress. It is a LIVE-only signal
	// (its value is "right now") — emitted to the console event channel but never
	// written to the durable JSONL, so the log holds exactly one serviced line per
	// request and Counts/seed-on-restart stay honest. Carries id/type; no status/dur.
	KindStarted = "started"
)

// Event sides: which actor an event is attributed to (drives the console pane).
const (
	SidePopo = "popo" // the controller (default when unset)
	SideNana = "nana" // the sandbox itself — output captured from running in it
)

// Event is one activity record. It is intentionally protocol-agnostic — the cli
// layer fills it from a protocol response without coupling protocol to logging.
type Event struct {
	TS     string `json:"ts"`
	Kind   string `json:"kind"`
	Side   string `json:"side,omitempty"` // "" / "popo" = controller, "nana" = sandbox echo
	ID     string `json:"id,omitempty"`
	Type   string `json:"type,omitempty"`
	Status string `json:"status,omitempty"`
	DurMS  int64  `json:"dur_ms,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Logger appends events to a rotating JSONL file and maintains a small cumulative-counts
// snapshot alongside it (so the console header seeds in O(1) and its counters survive log
// rotation instead of dropping when old lines age out). A zero-value path disables logging.
// Safe for concurrent Append (parallel dispatch) — the underlying logfile.Writer is
// thread- and process-safe, and the counts tally is mutex-guarded.
type Logger struct {
	path string
	w    *logfile.Writer

	mu       sync.Mutex
	served   int
	approved int
	denied   int
	last     time.Time
	dirty    int // counting-events since the last snapshot flush
}

// snapshotFlushEvery bounds how many serviced/approved/denied events accumulate before the
// counts snapshot is rewritten (a crash loses at most this many from the display counter; the
// durable log still holds them).
const snapshotFlushEvery = 8

// New returns a logger writing to path with the default rotation policy (empty path = no-op).
func New(path string) *Logger { return NewSized(path, logfile.DefaultMaxBytes, logfile.DefaultKeep) }

// NewSized is New with an explicit rotation policy (from config).
func NewSized(path string, maxBytes int64, keep int) *Logger {
	l := &Logger{path: path, w: logfile.New(path, maxBytes, keep)}
	if path != "" {
		s := SeedCounts(path) // continue the cumulative tally from the snapshot (or a one-time backfill)
		l.served, l.approved, l.denied, l.last = s.Served, s.Approved, s.Denied, s.LastAction
	}
	return l
}

// Path returns the file the logger writes to (empty if disabled).
func (l *Logger) Path() string { return l.path }

// Append stamps the event (UTC now if unset), writes it as one JSON line, and — for a
// serviced/approved/denied event — advances the cumulative counters and (throttled) persists the
// snapshot.
func (l *Logger) Append(e Event) error {
	if l.path == "" {
		return nil
	}
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := l.w.Append(append(line, '\n')); err != nil {
		return err
	}
	l.tally(e)
	return nil
}

// tally updates the cumulative counters for a real action event and flushes the snapshot on the
// throttle boundary.
func (l *Logger) tally(e Event) {
	switch e.Kind {
	case KindServiced, KindApproved, KindDenied:
	default:
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	switch e.Kind {
	case KindServiced:
		l.served++
	case KindApproved:
		l.approved++
	case KindDenied:
		l.denied++
	}
	if t, perr := time.Parse(time.RFC3339, e.TS); perr == nil {
		l.last = t
	}
	l.dirty++
	if l.dirty >= snapshotFlushEvery {
		l.dirty = 0
		_ = writeSnapshot(l.path, Snapshot{Served: l.served, Approved: l.approved, Denied: l.denied, LastAction: l.last})
	}
}

// Close flushes the counts snapshot and releases the log file. Idempotent.
func (l *Logger) Close() error {
	if l.path == "" {
		return nil
	}
	l.mu.Lock()
	if l.dirty > 0 {
		l.dirty = 0
		_ = writeSnapshot(l.path, Snapshot{Served: l.served, Approved: l.approved, Denied: l.denied, LastAction: l.last})
	}
	l.mu.Unlock()
	return l.w.Close()
}

// Read replays the durable log at path into its events (skipping blank/garbled
// lines). A missing file yields no events, not an error — so callers can seed from a
// not-yet-written log. This is the authoritative record for counters that must
// survive a console restart.
func Read(path string) ([]Event, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Event
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}

// Counts tallies the serviced/approved/denied totals from a replayed log — the
// authoritative seed for the console header's counters.
func Counts(events []Event) (serviced, approved, denied int) {
	for _, e := range events {
		switch e.Kind {
		case KindServiced:
			serviced++
		case KindApproved:
			approved++
		case KindDenied:
			denied++
		case KindStarted:
			// Live-only, never durable — but ignore defensively so a stray line (e.g.
			// a future writer or a hand-edited log) can't inflate the serviced tally.
		}
	}
	return
}

// LastAction returns the timestamp of the most recent operator-facing action —
// serviced/approved/denied, the same three the header counters track — or the zero time
// if the log holds none. Plumbing events (started, operated, verified) are deliberately
// excluded so the header's "last action" reflects real activity, not connection churn.
// Events are chronological (append-only), so the last match wins.
func LastAction(events []Event) time.Time {
	var last time.Time
	for _, e := range events {
		switch e.Kind {
		case KindServiced, KindApproved, KindDenied:
			if t, err := time.Parse(time.RFC3339, e.TS); err == nil {
				last = t
			}
		}
	}
	return last
}

// Snapshot is the cumulative counts sidecar kept next to the (rotating) activity log, so the
// console header seeds in O(1) and its counters don't drop when old log lines rotate away.
type Snapshot struct {
	Served     int       `json:"served"`
	Approved   int       `json:"approved"`
	Denied     int       `json:"denied"`
	LastAction time.Time `json:"last_action"`
}

// snapshotPath is the counts sidecar for an activity-log path.
func snapshotPath(logPath string) string { return logPath + ".counts.json" }

// writeSnapshot persists s atomically (tmp + rename) next to logPath. Best-effort.
func writeSnapshot(logPath string, s Snapshot) error {
	p := snapshotPath(logPath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ReadSnapshot returns the counts sidecar for logPath (ok=false if absent/unreadable).
func ReadSnapshot(logPath string) (Snapshot, bool) {
	data, err := os.ReadFile(snapshotPath(logPath))
	if err != nil {
		return Snapshot{}, false
	}
	var s Snapshot
	if json.Unmarshal(data, &s) != nil {
		return Snapshot{}, false
	}
	return s, true
}

// SeedCounts returns the cumulative counters for the header: the O(1) snapshot when present,
// otherwise a one-time backfill by replaying the durable log (Counts/LastAction) — which it then
// persists as the snapshot so subsequent seeds are O(1). A missing log yields zeros.
func SeedCounts(logPath string) Snapshot {
	if s, ok := ReadSnapshot(logPath); ok {
		return s
	}
	evs, _ := Read(logPath)
	served, approved, denied := Counts(evs)
	s := Snapshot{Served: served, Approved: approved, Denied: denied, LastAction: LastAction(evs)}
	_ = writeSnapshot(logPath, s)
	return s
}
