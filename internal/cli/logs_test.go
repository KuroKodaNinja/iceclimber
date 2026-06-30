package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/activity"
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

func TestEventBody_ShowsDuration(t *testing.T) {
	got := eventBody(activity.Event{Kind: activity.KindServiced, Type: "pip.install", Status: "ok", DurMS: 3200})
	if !strings.Contains(got, "3.2s") {
		t.Errorf("serviced body should show elapsed; got %q", got)
	}
}
