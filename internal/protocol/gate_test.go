package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestDispatchGate(t *testing.T) {
	ran := false
	reg := Registry{
		"python.install": func(_ context.Context, req Request) Response {
			ran = true
			return OK(req.ID, map[string]string{"version": "3.12.13"})
		},
	}
	d := NewDispatcher(nil, Tree{}, reg) // dispatch() never touches fs
	data, _ := json.Marshal(Request{SchemaVersion: 1, ID: "r1", Type: "python.install"})

	// No gate: handler runs.
	if resp := d.dispatch(context.Background(), "r1.json", data); resp.Status != StatusOK || !ran {
		t.Fatalf("no gate: status=%s ran=%v", resp.Status, ran)
	}

	// Deny gate: handler skipped, operator_denied returned.
	ran = false
	d.SetGate(func(_ context.Context, req Request) error {
		return fmt.Errorf("operator denied %s", req.Type)
	})
	resp := d.dispatch(context.Background(), "r1.json", data)
	if resp.Status != StatusError || resp.Error == nil || resp.Error.Code != "operator_denied" {
		t.Fatalf("deny gate: %+v", resp)
	}
	if ran {
		t.Error("handler ran despite deny gate")
	}

	// Allow gate: handler runs again.
	ran = false
	d.SetGate(func(_ context.Context, _ Request) error { return nil })
	if resp := d.dispatch(context.Background(), "r1.json", data); resp.Status != StatusOK || !ran {
		t.Fatalf("allow gate: status=%s ran=%v", resp.Status, ran)
	}
}
