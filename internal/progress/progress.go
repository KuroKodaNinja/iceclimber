// Package progress carries operator-facing progress for long install operations
// (runtime/package transfers). It is a leaf package (no internal deps) so any
// installer can report progress and the CLI/TUI can render it without an import
// cycle. Reporting is a simple callback; a throttled counting Reader turns an
// io.Reader into byte-progress events.
package progress

import (
	"fmt"
	"io"
	"time"
)

// Unit distinguishes byte progress (a transfer, shown as a bar + ETA) from step
// progress (e.g. package N of M, shown as a count).
type Unit int

const (
	Bytes Unit = iota
	Items
)

// Event is one progress sample. Total == 0 means indeterminate (render a spinner
// + phase only, never a fake bar).
type Event struct {
	Phase string // human label: "downloading", "transferring", "installing requests"
	Cur   int64
	Total int64
	Unit  Unit
}

// Func receives progress events. A nil Func is a no-op (installers stay silent on
// the CLI/agent paths that don't render progress).
type Func func(Event)

// Emit is a nil-safe send of a single event — for discrete phase markers
// ("resolving", "verifying") where there's no stream to count.
func (f Func) Emit(e Event) {
	if f != nil {
		f(e)
	}
}

// Phase is a convenience for an indeterminate phase marker (Total 0 → spinner).
func (f Func) Phase(name string) { f.Emit(Event{Phase: name}) }

// Reader wraps r and reports byte progress for phase via f, throttled to at most
// one event per ~100ms, plus a guaranteed final event at EOF. Total 0 =
// indeterminate. A nil Func returns r unwrapped (zero overhead).
func (f Func) Reader(r io.Reader, phase string, total int64) io.Reader {
	if f == nil {
		return r
	}
	return &reader{r: r, f: f, phase: phase, total: total, interval: 100 * time.Millisecond, now: time.Now}
}

type reader struct {
	r        io.Reader
	f        Func
	phase    string
	total    int64
	cur      int64
	interval time.Duration
	now      func() time.Time
	last     time.Time
	started  bool
}

func (rd *reader) Read(p []byte) (int, error) {
	n, err := rd.r.Read(p)
	if n > 0 {
		rd.cur += int64(n)
	}
	// Emit on the first read, then at most once per interval, and always at EOF so
	// the bar reaches 100% (and a consumer sees a terminal sample).
	now := rd.now()
	if !rd.started || err == io.EOF || now.Sub(rd.last) >= rd.interval {
		rd.started = true
		rd.last = now
		rd.f(Event{Phase: rd.phase, Cur: rd.cur, Total: rd.total, Unit: Bytes})
	}
	return n, err
}

// HumanBytes formats a byte count as a short human string (e.g. "78.2 MB").
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ETA estimates remaining time from the rate so far ("~9s", "~2m05s"). It returns
// "" when there isn't enough information to estimate (no total, not started, done).
func ETA(cur, total int64, elapsed time.Duration) string {
	if cur <= 0 || total <= 0 || cur >= total || elapsed <= 0 {
		return ""
	}
	rate := float64(cur) / elapsed.Seconds() // bytes/sec
	if rate <= 0 {
		return ""
	}
	rem := time.Duration(float64(total-cur) / rate * float64(time.Second))
	return "~" + shortDur(rem)
}

func shortDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()+0.5))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
