package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/wire"
)

func TestBuildParams(t *testing.T) {
	cases := []struct {
		verb string
		args []string
		want string // expected params JSON
	}{
		{"ping", nil, "null"},
		{"python.install", []string{"3.12"}, `{"version":"3.12"}`},
		{"java.install", []string{"21"}, `{"version":"21"}`},
		{"pip.install", []string{"--python", "3.12", "requests", "rich==13.7"},
			`{"packages":[{"name":"requests"},{"name":"rich","version":"13.7"}],"python_version":"3.12"}`},
		{"npm.install", []string{"--node", "24", "left-pad@1.3.0", "@scope/x"},
			`{"node_version":"24","packages":[{"name":"left-pad","version":"1.3.0"},{"name":"@scope/x"}]}`},
		{"maven.install", []string{"--java", "21", "com.google.code.gson:gson:2.10.1"},
			`{"java_version":"21","packages":[{"name":"com.google.code.gson:gson","version":"2.10.1"}]}`},
		{"web.fetch", []string{"https://x.test", "--method", "POST", "--header", "A: 1"},
			`{"headers":{"A":"1"},"method":"POST","url":"https://x.test"}`},
	}
	for _, c := range cases {
		p, err := buildParams(c.verb, c.args)
		if err != nil {
			t.Errorf("%s %v: %v", c.verb, c.args, err)
			continue
		}
		b, _ := json.Marshal(p)
		if string(b) != c.want {
			t.Errorf("%s %v →\n got %s\nwant %s", c.verb, c.args, b, c.want)
		}
	}
}

func TestBuildParams_Errors(t *testing.T) {
	bad := [][]string{}
	for _, tc := range []struct {
		verb string
		args []string
	}{
		{"python.install", nil},                                  // missing version
		{"pip.install", []string{"requests"}},                    // missing --python
		{"maven.install", []string{"--java", "21", "notacoord"}}, // bad coordinate
		{"web.fetch", nil},                                       // missing url
	} {
		if _, err := buildParams(tc.verb, tc.args); err == nil {
			bad = append(bad, tc.args)
		}
	}
	if len(bad) > 0 {
		t.Errorf("expected errors for: %v", bad)
	}
}

func TestPrintResult(t *testing.T) {
	cases := []struct {
		verb, result, want string
	}{
		{"python.install", `{"version":"3.12.13","path":"/r/runtimes/python/3.12.13/bin/python3","already_installed":true}`,
			"✓ python.install 3.12.13 → /r/runtimes/python/3.12.13/bin/python3 (already installed)\n"},
		{"pip.install", `{"installed":[{"name":"rich","version":"13.7","tier":"relay"}],"failed":[{"name":"foo","version":"1","error":"nope"}]}`,
			"✓ rich 13.7 (relay)\n✗ foo 1: nope\n"},
		{"web.fetch", `{"status_code":200,"venue":"sandbox-exec","body_blob":"protocol/blobs/abc"}`,
			"HTTP 200 (sandbox-exec)\nbody: /r/protocol/blobs/abc\n"},
		{"ping", `{"popo_version":"0.1.0","pong_at":"now"}`, "bridge up (Popo 0.1.0)\n"},
	}
	for _, c := range cases {
		var b bytes.Buffer
		printResult(&b, c.verb, "/r", json.RawMessage(c.result))
		if b.String() != c.want {
			t.Errorf("%s →\n got %q\nwant %q", c.verb, b.String(), c.want)
		}
	}
}

// TestRequestRoundTrip dogfoods the client end to end against a local stub Popo: it
// delivers a request, a goroutine acting as Popo picks it up and writes a response,
// and request() returns it — exercising envelope + atomic deliver + poll + parse.
func TestRequestRoundTrip(t *testing.T) {
	root := t.TempDir()
	tree := wire.Tree{Root: root}
	for _, d := range []string{tree.Outbox().Tmp(), tree.Outbox().New(), tree.Inbox().New()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Keep liveness fresh; stub Popo answers the first request it sees.
	_ = os.WriteFile(tree.Heartbeat(), []byte("1 t"), 0o644)
	go func() {
		for {
			entries, _ := os.ReadDir(tree.Outbox().New())
			for _, e := range entries {
				data, _ := os.ReadFile(filepath.Join(tree.Outbox().New(), e.Name()))
				var req wire.Request
				if json.Unmarshal(data, &req) != nil {
					continue
				}
				resp, _ := json.Marshal(wire.OK(req.ID, map[string]any{"popo_version": "test", "pong_at": "now"}))
				_ = os.WriteFile(filepath.Join(tree.Inbox().New(), e.Name()), resp, 0o644)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	resp, err := request(root, "ping", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.Status != wire.StatusOK || resp.ID == "" {
		t.Fatalf("response = %+v, want ok with an id", resp)
	}
}

func TestShellEnvBlock(t *testing.T) {
	got := shellEnvBlock("/home/agent/.iceclimber")
	want := "export ICECLIMBER_HOME='/home/agent/.iceclimber'\nexport PATH='/home/agent/.iceclimber':\"$PATH\"\n"
	if got != want {
		t.Errorf("shellEnvBlock:\n got %q\nwant %q", got, want)
	}
	// Single-quotes in the path are escaped so the block stays eval-safe.
	if q := shellQuote("/a'b"); q != `'/a'\''b'` {
		t.Errorf("shellQuote escape = %q", q)
	}
}

func TestResolveRoot_EnvWins(t *testing.T) {
	t.Setenv("ICECLIMBER_HOME", "/custom/root")
	if r, err := resolveRoot(); err != nil || r != "/custom/root" {
		t.Errorf("resolveRoot = %q, %v; want /custom/root", r, err)
	}
}
