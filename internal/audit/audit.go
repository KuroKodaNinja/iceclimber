// Package audit writes the controller-side, append-only audit log for outbound
// fetches (plan §6 security floor). Popo's copy is authoritative (§3); the log
// lives on the controller, one JSONL file per sandbox.
package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Entry is one audit record. Bodies are referenced by hash + size, not stored.
type Entry struct {
	TS         string `json:"ts"`
	ID         string `json:"id,omitempty"`
	Type       string `json:"type"`
	URL        string `json:"url"`
	Method     string `json:"method"`
	Venue      string `json:"venue"`
	StatusCode int    `json:"status_code"`
	BodySize   int    `json:"body_size"`
	BodySHA256 string `json:"body_sha256,omitempty"`
	Outcome    string `json:"outcome"` // "ok" | "error"
}

// Logger appends entries to a JSONL file. A zero-value path disables logging.
type Logger struct {
	path string
}

// New returns a logger writing to path (empty path = no-op).
func New(path string) *Logger { return &Logger{path: path} }

// Append stamps the entry with the current UTC time and appends it as one JSON
// line, creating the file and parent directory as needed.
func (l *Logger) Append(e Entry) error {
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
