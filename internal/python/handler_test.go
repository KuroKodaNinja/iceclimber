package python

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

// TestResolveLine: an omitted or "latest" version resolves to the recommended minor (so an
// agent installs the current runtime without guessing), while a specific minor is honored.
func TestResolveLine(t *testing.T) {
	for in, want := range map[string]string{
		"":       RecommendedLine,
		"latest": RecommendedLine,
		"LATEST": RecommendedLine,
		"3.11":   "3.11",
	} {
		if got := ResolveLine(in); got != want {
			t.Errorf("ResolveLine(%q) = %q, want %q", in, got, want)
		}
	}
	if RecommendedLine != "3.12" {
		t.Errorf("Python RecommendedLine = %q, want 3.12", RecommendedLine)
	}
}
