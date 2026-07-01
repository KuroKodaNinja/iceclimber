package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/probe"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs/remotefstest"
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

// TestServeDispatcher_PickupLine: the headless serve dispatcher prints a "▸ <type> …"
// receipt line at pickup (ephemeral — not appended to the JSONL), then the serviced
// line on completion.
func TestServeDispatcher_PickupLine(t *testing.T) {
	ctx := context.Background()
	fs := remotefs.NewExecFS(remotefstest.LocalRunner{})
	tree := protocol.Tree{Root: t.TempDir()}
	if err := protocol.EnsureTree(ctx, fs, tree); err != nil {
		t.Fatalf("EnsureTree: %v", err)
	}
	sess := &session{fs: fs, tree: tree, transport: "exec", sandboxID: "s", fp: &probe.Fingerprint{}}
	cfg := &config.Config{SandboxID: "s", ActivityLog: filepath.Join(t.TempDir(), "activity.jsonl")}
	var out bytes.Buffer
	disp := buildServeDispatcher(ctx, sess, cfg, nil, &out, false)

	id := protocol.NewID()
	name := protocol.RequestName(id)
	data, _ := json.Marshal(protocol.Request{
		SchemaVersion: protocol.SchemaVersion, ID: id, Type: "ping",
		CreatedAt: time.Now().UTC(), Params: json.RawMessage("{}"),
	})
	if err := protocol.Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
		t.Fatal(err)
	}
	if err := disp.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if s := out.String(); !strings.Contains(s, "▸ ping …") {
		t.Errorf("serve should print a pickup line; got %q", s)
	}
}

// TestBuildServeDispatcher_AppliesRetention guards the disp.SetRetention(cfg.Retention())
// wiring: an old uncollected response is reaped only if the builder actually applied the
// configured retention (delete the SetRetention line and this fails).
func TestBuildServeDispatcher_AppliesRetention(t *testing.T) {
	ctx := context.Background()
	fs := remotefs.NewExecFS(remotefstest.LocalRunner{})
	tree := protocol.Tree{Root: t.TempDir()}
	if err := protocol.EnsureTree(ctx, fs, tree); err != nil {
		t.Fatal(err)
	}
	sess := &session{fs: fs, tree: tree, transport: "exec", sandboxID: "s", fp: &probe.Fingerprint{}}
	cfg := &config.Config{SandboxID: "s", ActivityLog: filepath.Join(t.TempDir(), "a.jsonl"), MaildirRetention: "1h"}
	var out bytes.Buffer
	disp := buildServeDispatcher(ctx, sess, cfg, nil, &out, false)

	// An old (2h) uncollected response + its request — reaped iff retention is wired.
	name := protocol.RequestName(protocol.NewID())
	resp, _ := json.Marshal(protocol.Response{
		SchemaVersion: protocol.SchemaVersion, ID: strings.TrimSuffix(name, ".json"),
		Status: protocol.StatusOK, CompletedAt: time.Now().Add(-2 * time.Hour).UTC(),
	})
	if err := fs.WriteFile(ctx, filepath.Join(tree.Inbox().New(), name), resp); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(ctx, filepath.Join(tree.Outbox().Cur(), name), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := disp.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := fs.List(ctx, tree.Inbox().New()); len(n) != 0 {
		t.Error("buildServeDispatcher did not wire retention — old uncollected response not reaped")
	}
}
