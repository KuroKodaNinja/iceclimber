package progress

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestReader_CountsAndFlushes(t *testing.T) {
	var events []Event
	f := Func(func(e Event) { events = append(events, e) })
	// interval 0 → emit on every read; small reads to force several.
	rd := &reader{r: bytes.NewReader(make([]byte, 2500)), f: f, phase: "transferring", total: 2500, now: time.Now}

	buf := make([]byte, 1000)
	if _, err := io.CopyBuffer(io.Discard, struct{ io.Reader }{rd}, buf); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("no events emitted")
	}
	last := events[len(events)-1]
	if last.Cur != 2500 || last.Total != 2500 || last.Phase != "transferring" || last.Unit != Bytes {
		t.Errorf("final event = %+v, want full count 2500/2500", last)
	}
}

func TestReader_Throttle(t *testing.T) {
	var n int
	f := Func(func(Event) { n++ })
	clk := time.Unix(0, 0)
	// Frozen clock → only the first read and the EOF read emit (throttled between).
	rd := &reader{r: bytes.NewReader(make([]byte, 400)), f: f, phase: "p", total: 400,
		interval: time.Second, now: func() time.Time { return clk }}
	buf := make([]byte, 100)
	for {
		if _, err := rd.Read(buf); err == io.EOF {
			break
		}
	}
	if n != 2 { // first emit + EOF emit; the middle reads are throttled
		t.Errorf("emitted %d events, want 2 (first + EOF) under a frozen clock", n)
	}
}

func TestReader_NilFuncPassthrough(t *testing.T) {
	src := bytes.NewReader([]byte("hello"))
	if got := Func(nil).Reader(src, "p", 5); got != io.Reader(src) {
		t.Errorf("nil Func should return the reader unwrapped")
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{0: "0 B", 512: "512 B", 1024: "1.0 KB", 1536: "1.5 KB", 125 * 1024 * 1024: "125.0 MB"}
	for in, want := range cases {
		if got := HumanBytes(in); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestETA(t *testing.T) {
	// 50/100 bytes in 5s → ~5s remaining.
	if got := ETA(50, 100, 5*time.Second); got != "~5s" {
		t.Errorf("ETA = %q, want ~5s", got)
	}
	// Indeterminate / done / no-elapsed → empty.
	for _, c := range []struct {
		cur, total int64
		el         time.Duration
	}{{0, 100, time.Second}, {100, 100, time.Second}, {50, 0, time.Second}, {50, 100, 0}} {
		if got := ETA(c.cur, c.total, c.el); got != "" {
			t.Errorf("ETA(%d,%d,%v) = %q, want empty", c.cur, c.total, c.el, got)
		}
	}
	// Minutes formatting: 10/250 B in 5s → 2 B/s, 240 B left → 120s = ~2m00s.
	if got := ETA(10, 250, 5*time.Second); !strings.HasPrefix(got, "~2m") {
		t.Errorf("ETA minutes = %q, want ~2m..", got)
	}
}
