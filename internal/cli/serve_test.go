package cli

import (
	"encoding/json"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
)

func TestServiceDetail(t *testing.T) {
	raw := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	tests := []struct {
		name    string
		reqType string
		resp    protocol.Response
		want    string
	}{
		{
			"python", "python.install",
			protocol.Response{Status: "ok", Result: raw(map[string]any{"version": "3.12.13", "path": "/x/bin/python3"})},
			"python 3.12.13",
		},
		{
			"pip", "pip.install",
			protocol.Response{Status: "ok", Result: raw(map[string]any{"installed": []any{
				map[string]any{"name": "rich", "version": "15.0.0", "tier": "relay"},
			}})},
			"rich 15.0.0 (relay)",
		},
		{
			"webfetch", "web.fetch",
			protocol.Response{Status: "ok", Result: raw(map[string]any{"status_code": 200, "venue": "controller"})},
			"200 controller",
		},
		{
			"held", "web.fetch",
			protocol.Response{Status: protocol.StatusNeedsClarification, Clarification: &protocol.Clarification{Question: "approve f1"}},
			"approve f1",
		},
		{
			"error", "python.install",
			protocol.Response{Status: protocol.StatusError, Error: &protocol.Error{Code: "resolution_failed", Message: "no such version"}},
			"resolution_failed: no such version",
		},
		{
			"ping yields nothing useful", "ping",
			protocol.Response{Status: "ok", Result: raw(map[string]any{"pong_at": "x", "popo_version": "0.1"})},
			"",
		},
		{
			"unknown verb is safe", "mystery.verb",
			protocol.Response{Status: "ok", Result: raw(map[string]any{"foo": "bar"})},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := serviceDetail(tt.reqType, tt.resp); got != tt.want {
				t.Errorf("serviceDetail(%q) = %q, want %q", tt.reqType, got, tt.want)
			}
		})
	}
}
