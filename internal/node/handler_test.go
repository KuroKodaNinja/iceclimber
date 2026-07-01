package node

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// Malformed params return before the installer is used, so a nil installer is fine.
func TestHandlerErrors(t *testing.T) {
	h := Handler(nil)
	if r := h(context.Background(), protocol.Request{ID: "1", Params: json.RawMessage("{")}); r.Error == nil || r.Error.Code != "malformed_params" {
		t.Errorf("malformed params: got %+v", r.Error)
	}
}

// TestResolveLine: an omitted or "latest" version resolves to the recommended line (so an
// agent installs the current runtime without guessing), while a specific line is honored.
func TestResolveLine(t *testing.T) {
	for in, want := range map[string]string{
		"":         RecommendedLine,
		"  ":       RecommendedLine,
		"latest":   RecommendedLine,
		"LATEST":   RecommendedLine,
		" latest ": RecommendedLine,
		"22":       "22",
		"20.11":    "20.11",
	} {
		if got := ResolveLine(in); got != want {
			t.Errorf("ResolveLine(%q) = %q, want %q", in, got, want)
		}
	}
	if RecommendedLine != "24" {
		t.Errorf("Node RecommendedLine = %q, want 24 (musl arm64 needs ≥ 24)", RecommendedLine)
	}
}
