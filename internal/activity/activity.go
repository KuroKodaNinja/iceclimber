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
	"time"
)

// Event kinds.
const (
	KindServiced = "serviced" // Popo serviced a request (id/type/status/dur)
	KindApproved = "approved" // operator approved a held egress
	KindDenied   = "denied"   // operator denied a held egress
	KindOperated = "operated" // operator-initiated action (console install/bootstrap)
)

// Event is one activity record. It is intentionally protocol-agnostic — the cli
// layer fills it from a protocol response without coupling protocol to logging.
type Event struct {
	TS     string `json:"ts"`
	Kind   string `json:"kind"`
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
