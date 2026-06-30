package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/progress"
)

func TestProgressPrinter_TTY(t *testing.T) {
	var buf bytes.Buffer
	clk := time.Unix(100, 0)
	p := &progressPrinter{w: &buf, tty: true, transport: "exec", now: func() time.Time { return clk }}

	p.handle(progress.Event{Phase: "transferring", Cur: 62, Total: 100, Unit: progress.Bytes})
	out := buf.String()
	for _, want := range []string{"transferring", "62%", "via exec", "\r"} {
		if !strings.Contains(out, want) {
			t.Errorf("TTY bytes line %q missing %q", out, want)
		}
	}

	buf.Reset()
	p.handle(progress.Event{Phase: "installing requests", Cur: 3, Total: 5, Unit: progress.Items})
	if got := buf.String(); !strings.Contains(got, "(3/5)") || !strings.Contains(got, "installing requests") {
		t.Errorf("TTY items line = %q, want (3/5)", got)
	}

	buf.Reset()
	p.finish()
	if !strings.Contains(buf.String(), "\x1b[K") {
		t.Errorf("finish should clear the line, got %q", buf.String())
	}
}

func TestProgressPrinter_Piped(t *testing.T) {
	var buf bytes.Buffer
	p := &progressPrinter{w: &buf, tty: false, transport: "sftp", now: time.Now}

	// Piped: one line per phase transition, no escape codes, no bar.
	p.handle(progress.Event{Phase: "downloading", Total: 100, Unit: progress.Bytes})
	p.handle(progress.Event{Phase: "downloading", Cur: 50, Total: 100, Unit: progress.Bytes}) // same phase → no extra line
	p.handle(progress.Event{Phase: "transferring", Total: 100, Unit: progress.Bytes})
	p.finish()

	out := buf.String()
	if strings.Contains(out, "\x1b[") || strings.Contains(out, "\r") {
		t.Errorf("piped output must have no escape codes / carriage returns: %q", out)
	}
	if n := strings.Count(out, "\n"); n != 2 {
		t.Errorf("want one line per phase (2), got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "downloading · via sftp…") || !strings.Contains(out, "transferring · via sftp…") {
		t.Errorf("piped phase lines missing: %q", out)
	}
}
