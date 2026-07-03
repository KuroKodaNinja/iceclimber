// Package audit writes the controller-side, append-only audit log for outbound
// fetches (plan §6 security floor). Popo's copy is authoritative (§3); the log
// lives on the controller, one JSONL file per sandbox.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/logfile"
)

// Entry is one audit record. Bodies are referenced by hash + size, not stored.
type Entry struct {
	TS           string `json:"ts"`
	ID           string `json:"id,omitempty"`
	Type         string `json:"type"`
	URL          string `json:"url"`
	Method       string `json:"method"`
	RewrittenURL string `json:"rewritten_url,omitempty"`
	Venue        string `json:"venue"`
	Decision     string `json:"decision,omitempty"` // allow | held | denied (controller venue)
	StatusCode   int    `json:"status_code"`
	BodySize     int    `json:"body_size"`
	BodySHA256   string `json:"body_sha256,omitempty"`
	Outcome      string `json:"outcome"` // "ok" | "error"
}

// Logger appends entries to a rotating JSONL file. A zero-value path disables logging. Safe for
// concurrent Append (parallel web.fetch) — the underlying logfile.Writer is thread/process-safe.
type Logger struct {
	path string
	w    *logfile.Writer
}

// New returns a logger writing to path with the default rotation policy (empty path = no-op).
func New(path string) *Logger { return NewSized(path, logfile.DefaultMaxBytes, logfile.DefaultKeep) }

// NewSized is New with an explicit rotation policy (from config).
func NewSized(path string, maxBytes int64, keep int) *Logger {
	return &Logger{path: path, w: logfile.New(path, maxBytes, keep)}
}

// Path returns the file the logger writes to (empty if disabled).
func (l *Logger) Path() string { return l.path }

// Append stamps the entry with the current UTC time (if unset) and appends it as one JSON line.
func (l *Logger) Append(e Entry) error {
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
	return l.w.Append(append(line, '\n'))
}

// Close releases the log file. Idempotent.
func (l *Logger) Close() error {
	if l.path == "" {
		return nil
	}
	return l.w.Close()
}

// Body renders the human one-line text of an entry: method + target URL, the venue it
// was served from (controller|sandbox-exec), the controller decision (allow|held|denied),
// and the outcome (status code or error). Shared by `iceclimber logs` ([EGRESS] lines),
// the console's live feed, and the egress panel's recent-fetch list — one rendering, no
// three-way drift.
func Body(e Entry) string {
	method := e.Method
	if method == "" {
		method = "GET"
	}
	head := fmt.Sprintf("%-4s %s", method, e.URL)
	if e.Venue != "" {
		head += "  (" + e.Venue + ")"
	}
	parts := []string{head}
	if e.Decision != "" {
		parts = append(parts, e.Decision)
	}
	if e.Outcome == "error" {
		parts = append(parts, "error")
	} else if e.StatusCode > 0 {
		parts = append(parts, fmt.Sprintf("%d", e.StatusCode))
	}
	return strings.Join(parts, " · ")
}

// Read replays the audit log at path into its entries (skipping blank/garbled lines). A missing
// file yields no entries, not an error — so `iceclimber logs` can surface the audit feed even
// before the first fetch. Mirrors activity.Read.
func Read(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Entry
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}
