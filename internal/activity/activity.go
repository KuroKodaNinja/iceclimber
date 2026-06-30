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
	"time"
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

// Logger appends events to a JSONL file. A zero-value path disables logging.
type Logger struct {
	path string
}

// New returns a logger writing to path (empty path = no-op).
func New(path string) *Logger { return &Logger{path: path} }

// Path returns the file the logger writes to (empty if disabled).
func (l *Logger) Path() string { return l.path }

// Append stamps the event with the current UTC time (if unset) and appends it as
// one JSON line, creating the file and parent directory as needed.
func (l *Logger) Append(e Event) error {
	if l.path == "" {
		return nil
	}
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
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
