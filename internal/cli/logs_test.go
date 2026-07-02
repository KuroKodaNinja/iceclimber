package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
	"github.com/KuroKodaNinja/iceclimber/internal/audit"
)

func TestRenderActivity(t *testing.T) {
	tests := []struct {
		name string
		ev   activity.Event
		want string
	}{
		{"serviced", activity.Event{TS: "2026-06-28T12:00:00Z", Kind: activity.KindServiced, Type: "python.install", Status: "ok", Detail: "python 3.12.13"}, "python.install"},
		{"approved", activity.Event{TS: "2026-06-28T12:00:00Z", Kind: activity.KindApproved, Detail: "https://xkcd.com/info.0.json"}, "approved https://xkcd.com/info.0.json"},
		{"denied", activity.Event{TS: "2026-06-28T12:00:00Z", Kind: activity.KindDenied, Detail: "https://evil/"}, "denied https://evil/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := json.Marshal(tt.ev)
			got := renderActivity(string(b))
			if !strings.HasPrefix(got, "[POPO] ") || !strings.Contains(got, tt.want) {
				t.Errorf("renderActivity = %q, want [POPO]-prefixed containing %q", got, tt.want)
			}
		})
	}
	if got := renderActivity("not json"); got != "" {
		t.Errorf("unparseable line should render empty, got %q", got)
	}
}

// TestAuditBody renders an egress audit entry as the [EGRESS] body: method + venue,
// the controller decision, and the outcome (status or error).
func TestAuditBody(t *testing.T) {
	got := auditBody(audit.Entry{Method: "GET", URL: "https://example.com/x", Venue: "sandbox-exec", Decision: "allow", StatusCode: 200, Outcome: "ok"})
	for _, want := range []string{"GET", "https://example.com/x", "sandbox-exec", "allow", "200"} {
		if !strings.Contains(got, want) {
			t.Errorf("auditBody = %q, want it to contain %q", got, want)
		}
	}
	// An errored fetch shows "error", not a status code.
	if got := auditBody(audit.Entry{Method: "POST", URL: "https://down.example", Venue: "controller", Outcome: "error"}); !strings.Contains(got, "error") || strings.Contains(got, "· 0") {
		t.Errorf("errored auditBody = %q, want it to show error and no bogus status", got)
	}
	// Defaults the method to GET when unset.
	if got := auditBody(audit.Entry{URL: "https://x/y", Venue: "controller", StatusCode: 204, Outcome: "ok"}); !strings.Contains(got, "GET") || !strings.Contains(got, "https://x/y") {
		t.Errorf("auditBody method fallback = %q", got)
	}
}

// TestEgressFeedLine_SinceFilter: [EGRESS] lines older than the cutoff are dropped;
// newer ones render with the [EGRESS] tag.
func TestEgressFeedLine_SinceFilter(t *testing.T) {
	old := audit.Entry{TS: "2020-01-01T00:00:00Z", Method: "GET", Venue: "old.example", Decision: "allow", StatusCode: 200}
	recent := audit.Entry{TS: time.Now().UTC().Format(time.RFC3339), Method: "GET", Venue: "new.example", Decision: "allow", StatusCode: 200}
	cutoff := time.Now().Add(-time.Hour)

	ob, _ := json.Marshal(old)
	if _, ok := egressFeedLine(string(ob), cutoff); ok {
		t.Error("old egress entry should be filtered out by --since")
	}
	rb, _ := json.Marshal(recent)
	fl, ok := egressFeedLine(string(rb), cutoff)
	if !ok || !strings.HasPrefix(fl.s, "[EGRESS] ") || !strings.Contains(fl.s, "new.example") {
		t.Errorf("recent egress line = %q ok=%v, want [EGRESS]-tagged", fl.s, ok)
	}
	if _, ok := egressFeedLine("not json", cutoff); ok {
		t.Error("garbled egress line should be skipped")
	}
}

// TestPopoFeedLine_KindFilter: the --kind set restricts which activity kinds render.
func TestPopoFeedLine_KindFilter(t *testing.T) {
	serviced := activity.Event{TS: "2026-06-28T12:00:00Z", Kind: activity.KindServiced, Type: "pip.install", Status: "ok"}
	denied := activity.Event{TS: "2026-06-28T12:00:00Z", Kind: activity.KindDenied, Detail: "https://evil/"}
	only := map[string]bool{activity.KindDenied: true}

	sb, _ := json.Marshal(serviced)
	if _, ok := popoFeedLine(string(sb), time.Time{}, only); ok {
		t.Error("serviced should be filtered out when --kind=denied")
	}
	db, _ := json.Marshal(denied)
	if fl, ok := popoFeedLine(string(db), time.Time{}, only); !ok || !strings.HasPrefix(fl.s, "[POPO] ") {
		t.Errorf("denied line = %q ok=%v, want kept + [POPO]-tagged", fl.s, ok)
	}
	// nil kind set = all kinds pass.
	if _, ok := popoFeedLine(string(sb), time.Time{}, nil); !ok {
		t.Error("nil kind set should pass all kinds")
	}
}

// TestSourceFilter: --only restricts sources; empty = all.
func TestSourceFilter(t *testing.T) {
	all := sourceFilter(nil)
	for _, s := range []string{"popo", "nana", "egress"} {
		if !all(s) {
			t.Errorf("empty --only should allow %q", s)
		}
	}
	only := sourceFilter([]string{"egress", "nana"})
	if only("popo") {
		t.Error("--only=egress,nana should exclude popo")
	}
	if !only("egress") || !only("nana") {
		t.Error("--only=egress,nana should include egress and nana")
	}
}

func TestEventBody_ShowsDuration(t *testing.T) {
	got := eventBody(activity.Event{Kind: activity.KindServiced, Type: "pip.install", Status: "ok", DurMS: 3200})
	if !strings.Contains(got, "3.2s") {
		t.Errorf("serviced body should show elapsed; got %q", got)
	}
}

func TestEventBody_StartedFallback(t *testing.T) {
	// KindStarted is live-only; eventBody renders a stray on-disk line defensively,
	// with a "?" type fallback.
	if got := eventBody(activity.Event{Kind: activity.KindStarted, Type: "pip.install"}); !strings.Contains(got, "▸ pip.install …") {
		t.Errorf("started body should show the type; got %q", got)
	}
	if got := eventBody(activity.Event{Kind: activity.KindStarted}); !strings.Contains(got, "▸ ? …") {
		t.Errorf("started body with empty type should render the ? fallback; got %q", got)
	}
}
