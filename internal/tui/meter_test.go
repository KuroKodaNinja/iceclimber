package tui

import (
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/progress"
)

func TestMeterBar_Clamps(t *testing.T) {
	// Out-of-range ratios must not panic (a negative w-n would panic strings.Repeat)
	// and must stay a fixed width.
	for _, r := range []float64{-0.5, 0, 0.5, 1, 1.5} {
		bar := meterBar(r, 18)
		if got := len([]rune(bar)); got != 20 { // 18 cells + the two ▕▏ caps
			t.Errorf("meterBar(%v) width = %d runes, want 20", r, got)
		}
	}
}

func TestPct_Clamps(t *testing.T) {
	for in, want := range map[float64]int{-0.1: 0, 0: 0, 0.625: 62, 1: 100, 1.5: 100} {
		if got := pct(in); got != want {
			t.Errorf("pct(%v) = %d, want %d", in, got, want)
		}
	}
}

// TestRenderMeter covers the branches the teatest happy-path doesn't: nil sample,
// indeterminate (Total 0), bytes-with-bar, and items.
func TestRenderMeter(t *testing.T) {
	base := func(p *ProgressMsg) Console {
		c := NewConsole("sbx", nil, "", nil)
		c.running = "Python install"
		c.prog = p
		return c
	}
	if got := base(nil).renderMeter(); !strings.Contains(got, "Python install") || !strings.HasSuffix(got, "…") {
		t.Errorf("nil sample = %q, want spinner+label+…", got)
	}
	if got := base(&ProgressMsg{Event: progress.Event{Phase: "resolving"}}).renderMeter(); !strings.Contains(got, "resolving") || strings.Contains(got, "%") {
		t.Errorf("indeterminate = %q, want phase only, no bar/%%", got)
	}
	if got := base(&ProgressMsg{Event: progress.Event{Phase: "transferring", Cur: 50, Total: 100, Unit: progress.Bytes}, Transport: "exec"}).renderMeter(); !strings.Contains(got, "50%") || !strings.Contains(got, "via exec") {
		t.Errorf("bytes = %q, want 50%% + via exec", got)
	}
	if got := base(&ProgressMsg{Event: progress.Event{Phase: "installing six", Cur: 2, Total: 5, Unit: progress.Items}}).renderMeter(); !strings.Contains(got, "(2/5)") {
		t.Errorf("items = %q, want (2/5)", got)
	}
}
