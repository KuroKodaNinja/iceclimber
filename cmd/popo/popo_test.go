package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
		{"npm.install", []string{"--node", "24", "--project", "/tmp/app"},
			`{"node_version":"24","project":"/tmp/app"}`},
		{"maven.install", []string{"--java", "21", "com.google.code.gson:gson:2.10.1"},
			`{"java_version":"21","packages":[{"name":"com.google.code.gson:gson","version":"2.10.1"}]}`},
		{"maven.build", []string{"--java", "21", "--project", "/tmp/app"},
			`{"java_version":"21","project":"/tmp/app"}`},
		{"maven.build", []string{"--java", "21", "--project", "/tmp/app", "clean", "package"},
			`{"goals":["clean","package"],"java_version":"21","project":"/tmp/app"}`},
		{"conda.install", []string{"--python", "3.12", "-c", "conda-forge", "numpy=1.26", "six"},
			`{"extra_args":["-c","conda-forge"],"packages":[{"name":"numpy","version":"1.26"},{"name":"six"}],"python_version":"3.12"}`},
		{"conda.install", []string{"--python", "3.12", "--offline", "-c", "conda-forge", "six"},
			`{"extra_args":["-c","conda-forge","--offline"],"packages":[{"name":"six"}],"python_version":"3.12"}`},
		{"conda.install", []string{"--file", "/p/environment.yml"},
			`{"file":"/p/environment.yml"}`},
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
		{"python.install", nil},                                                 // missing version
		{"pip.install", []string{"requests"}},                                   // missing --python
		{"conda.install", []string{"numpy"}},                                    // missing --python
		{"conda.install", []string{"--file", "/p/env.yml", "--python", "3.12"}}, // --file + --python
		{"npm.install", []string{"--node", "24", "--project", "/p", "extra"}},   // --project + packages
		{"maven.install", []string{"--java", "21", "notacoord"}},                // bad coordinate
		{"web.fetch", nil}, // missing url
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
	for _, d := range []string{tree.Outbox().Tmp(), tree.Outbox().New(), tree.Inbox().New(), tree.Inbox().Cur()} {
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
	// await auto-collects: the response is moved out of inbox/new into inbox/cur so Popo
	// can prune the pair and inbox/new reflects only uncollected mail.
	name := wire.RequestName(resp.ID)
	if _, err := os.Stat(filepath.Join(tree.Inbox().New(), name)); !os.IsNotExist(err) {
		t.Error("await should have collected the response out of inbox/new")
	}
	if _, err := os.Stat(filepath.Join(tree.Inbox().Cur(), name)); err != nil {
		t.Errorf("collected response should be in inbox/cur: %v", err)
	}
}

func TestCollect_MovesResponse(t *testing.T) {
	root := t.TempDir()
	tree := wire.Tree{Root: root}
	for _, d := range []string{tree.Inbox().New(), tree.Inbox().Cur()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	name := wire.RequestName("01TESTID")
	if err := os.WriteFile(filepath.Join(tree.Inbox().New(), name), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := collect(tree, name); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree.Inbox().New(), name)); !os.IsNotExist(err) {
		t.Error("collect should remove the response from inbox/new")
	}
	if _, err := os.Stat(filepath.Join(tree.Inbox().Cur(), name)); err != nil {
		t.Errorf("collect should place the response in inbox/cur: %v", err)
	}
}

func TestCollectCmd(t *testing.T) {
	root := t.TempDir()
	tree := wire.Tree{Root: root}
	for _, d := range []string{tree.Inbox().New(), tree.Inbox().Cur()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Usage errors on the wrong arg count.
	if err := collectCmd(root, nil); err == nil {
		t.Error("collect with no id should error")
	}
	if err := collectCmd(root, []string{"a", "b"}); err == nil {
		t.Error("collect with two args should error")
	}
	// Success moves the response into inbox/cur.
	name := wire.RequestName("01COLLECTID")
	if err := os.WriteFile(filepath.Join(tree.Inbox().New(), name), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := collectCmd(root, []string{"01COLLECTID"}); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree.Inbox().Cur(), name)); err != nil {
		t.Errorf("response not moved to inbox/cur: %v", err)
	}
	// Idempotent: collecting an already-collected/absent id is success, not an error
	// (the request/await flow already auto-collected it).
	if err := collectCmd(root, []string{"01COLLECTID"}); err != nil {
		t.Errorf("re-collect should be idempotent, got %v", err)
	}
}

func TestAwait_CollectFailureNonFatal(t *testing.T) {
	// inbox/cur intentionally absent → the auto-collect rename fails, but await must still
	// return the response (collection is best-effort; a failed collect just leaves it
	// counted as uncollected).
	root := t.TempDir()
	tree := wire.Tree{Root: root}
	for _, d := range []string{tree.Outbox().Tmp(), tree.Outbox().New(), tree.Inbox().New()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
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
				resp, _ := json.Marshal(wire.OK(req.ID, map[string]any{"popo_version": "test"}))
				_ = os.WriteFile(filepath.Join(tree.Inbox().New(), e.Name()), resp, 0o644)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	resp, err := request(root, "ping", nil)
	if err != nil {
		t.Fatalf("await must return the response even when collect fails: %v", err)
	}
	if resp.Status != wire.StatusOK {
		t.Errorf("response = %+v, want ok", resp)
	}
}

func TestShellEnvBlock(t *testing.T) {
	got := shellEnvBlock("/home/agent/.iceclimber")
	want := "export ICECLIMBER_HOME='/home/agent/.iceclimber'\nexport PATH='/home/agent/.iceclimber':\"$PATH\"\n" +
		"[ -f '/home/agent/.iceclimber'/egress-env.sh ] && . '/home/agent/.iceclimber'/egress-env.sh\n"
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

func TestHelp_ListsShellenv(t *testing.T) {
	if h := helpText(""); !strings.Contains(h, "shellenv") {
		t.Errorf("`popo help` should list shellenv:\n%s", h)
	}
}
