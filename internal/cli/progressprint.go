package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/progress"
)

// progressPrinter renders install progress to a writer: a single-line, \r-updating
// meter on a TTY (phase + %/bytes/ETA or (i/n) + transfer mode), or one plain line
// per phase transition when piped (no escape codes). It's the CLI analogue of the
// console's animated footer.
type progressPrinter struct {
	w         io.Writer
	tty       bool
	transport string
	now       func() time.Time
	phase     string
	start     time.Time
	active    bool
}

// installProgress builds a progress.Func for w plus a finish() that clears the
// in-progress line (TTY) so the caller's result line prints cleanly. A non-file
// writer (or non-terminal) is treated as piped.
func installProgress(w io.Writer, transport string) (progress.Func, func()) {
	pp := &progressPrinter{w: w, transport: transport, now: time.Now}
	if f, ok := w.(*os.File); ok {
		pp.tty = isTerminal(f)
	}
	return pp.handle, pp.finish
}

func (p *progressPrinter) handle(e progress.Event) {
	if e.Phase != p.phase {
		p.phase = e.Phase
		p.start = p.now()
		if !p.tty { // piped: one line per phase, no bar/escape codes
			fmt.Fprintf(p.w, "%s%s…\n", e.Phase, p.via())
			return
		}
	}
	if !p.tty {
		return
	}
	p.active = true
	line := e.Phase
	switch {
	case e.Unit == progress.Bytes && e.Total > 0:
		line += fmt.Sprintf("  %d%%  %s/%s", e.Cur*100/e.Total, progress.HumanBytes(e.Cur), progress.HumanBytes(e.Total))
		if eta := progress.ETA(e.Cur, e.Total, p.now().Sub(p.start)); eta != "" {
			line += "  " + eta
		}
	case e.Unit == progress.Items && e.Total > 0:
		line += fmt.Sprintf("  (%d/%d)", e.Cur, e.Total)
	}
	line += p.via()
	fmt.Fprintf(p.w, "\r\x1b[K%s", line) // carriage return + clear-to-EOL
}

func (p *progressPrinter) via() string {
	if p.transport == "" {
		return ""
	}
	return " · via " + p.transport
}

func (p *progressPrinter) finish() {
	if p.tty && p.active {
		fmt.Fprint(p.w, "\r\x1b[K")
	}
}
