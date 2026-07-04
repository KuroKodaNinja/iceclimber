package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

// TestHandler covers the tool.install verb's error paths — the ones that need no live sandbox:
// malformed params, a missing/empty tool name, an unknown tool (whose message lists the available
// tools), and a catalog load failure.
func TestHandler(t *testing.T) {
	cat, err := Load(nil)
	if err != nil {
		t.Fatalf("load embedded catalog: %v", err)
	}
	loadOK := func() (*Catalog, error) { return cat, nil }
	loadFail := func() (*Catalog, error) { return nil, fmt.Errorf("boom") }

	tests := []struct {
		name       string
		load       func() (*Catalog, error)
		params     json.RawMessage
		wantCode   string
		wantMsgHas string
	}{
		{"malformed params", loadOK, json.RawMessage("{"), "malformed_params", ""},
		{"missing params", loadOK, nil, "malformed_params", "name"},
		{"empty name", loadOK, json.RawMessage(`{"name":""}`), "malformed_params", "name"},
		{"unknown tool lists alternatives", loadOK, json.RawMessage(`{"name":"nonesuch"}`), "unknown_tool", "bash"},
		{"catalog load failure", loadFail, json.RawMessage(`{"name":"bash"}`), "catalog_error", "boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := Handler(tc.load, InstallDeps{})
			resp := h(context.Background(), protocol.Request{ID: "r1", Params: tc.params})
			if resp.Status != protocol.StatusError || resp.Error == nil {
				t.Fatalf("resp = %+v, want an error response", resp)
			}
			if resp.Error.Code != tc.wantCode {
				t.Errorf("error code = %q, want %q", resp.Error.Code, tc.wantCode)
			}
			if tc.wantMsgHas != "" && !strings.Contains(resp.Error.Message, tc.wantMsgHas) {
				t.Errorf("error message = %q, want it to contain %q", resp.Error.Message, tc.wantMsgHas)
			}
		})
	}
}
