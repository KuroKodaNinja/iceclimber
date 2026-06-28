// Package tail follows an append-only file, returning complete lines as they
// arrive. It is the shared substrate for `iceclimber logs` and the TUI: poll the
// activity-log JSONL (and the agent's output stream) without re-reading from the
// start, withholding a partial trailing line until its newline lands and resetting
// on truncation/rotation. Poll-based (no fsnotify); the caller decides the cadence.
package tail

import (
	"io"
	"os"
	"strings"
)

// Reader tails one file, remembering where it last read to.
type Reader struct {
	path   string
	offset int64
}

// NewReader returns a tailer for path (the file need not exist yet).
func NewReader(path string) *Reader { return &Reader{path: path} }

// History returns all current complete lines and advances the read position to the
// end, so a following Poll continues from there. A missing file yields no lines.
func (r *Reader) History() []string {
	data, err := os.ReadFile(r.path)
	if err != nil {
		return nil
	}
	r.offset = int64(len(data))
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// Poll returns complete lines appended since the last read. It consumes only up to
// the last newline (a partial trailing line waits for the next call) and resets to
// the start if the file shrank (truncation/rotation).
func (r *Reader) Poll() []string {
	f, err := os.Open(r.path)
	if err != nil {
		return nil // not created yet; keep waiting
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	size := fi.Size()
	if size < r.offset {
		r.offset = 0
	}
	if size == r.offset {
		return nil
	}
	if _, err := f.Seek(r.offset, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	text := string(data)
	lastNL := strings.LastIndexByte(text, '\n')
	if lastNL < 0 {
		return nil // no complete line yet
	}
	consumed := text[:lastNL+1]
	r.offset += int64(len(consumed))
	return strings.Split(strings.TrimRight(consumed, "\n"), "\n")
}

// LastN returns the last n elements of lines (n <= 0 returns all).
func LastN(lines []string, n int) []string {
	if n <= 0 || n >= len(lines) {
		return lines
	}
	return lines[len(lines)-n:]
}
