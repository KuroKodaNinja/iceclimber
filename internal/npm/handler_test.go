package npm

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

func TestHandlerErrors(t *testing.T) {
	h := Handler(Deps{})
	if r := h(context.Background(), protocol.Request{ID: "1", Params: json.RawMessage("{")}); r.Error == nil || r.Error.Code != "malformed_params" {
		t.Errorf("malformed params: got %+v", r.Error)
	}
	if r := h(context.Background(), protocol.Request{ID: "2", Params: json.RawMessage("{}")}); r.Error == nil || r.Error.Code != "missing_node_version" {
		t.Errorf("missing version: got %+v", r.Error)
	}
	if r := h(context.Background(), protocol.Request{ID: "3", Params: json.RawMessage(`{"node_version":"x"}`)}); r.Error == nil || r.Error.Code != "no_packages" {
		t.Errorf("no packages: got %+v", r.Error)
	}
}
