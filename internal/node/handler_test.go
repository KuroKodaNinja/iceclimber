package node

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// Error paths return before the installer is used, so a nil installer is fine.
func TestHandlerErrors(t *testing.T) {
	h := Handler(nil)
	if r := h(context.Background(), protocol.Request{ID: "1", Params: json.RawMessage("{")}); r.Error == nil || r.Error.Code != "malformed_params" {
		t.Errorf("malformed params: got %+v", r.Error)
	}
	if r := h(context.Background(), protocol.Request{ID: "2", Params: json.RawMessage("{}")}); r.Error == nil || r.Error.Code != "missing_version" {
		t.Errorf("missing version: got %+v", r.Error)
	}
}
