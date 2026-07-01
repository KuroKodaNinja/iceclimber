//go:build scenario

// Package nodeapp is a self-contained, full-stack application scenario: it
// provisions a Node runtime and npm packages in the sandbox, fetches data through
// Popo, builds and runs a real Node program, and asserts its computed output. See
// README.md in this directory. Run with `make scenario`.
package nodeapp

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/test/scenarios/harness"
)

//go:embed app/index.js
var appJS []byte

//go:embed app/package.json
var appPkgJSON []byte

// TestNodeApp exercises the whole Node stack end to end as a real npm project:
// web.fetch (through Popo) → node.install → a package.json project installed via the
// manifest-driven relay (`install npm --project`, resolving blessed + blessed-contrib —
// whose node_modules carries .bin symlinks) → build + run the blessed-contrib dashboard
// with ordinary local ./node_modules resolution → assert the computed report.
func TestNodeApp(t *testing.T) {
	sb := harness.Require(t)
	root := sb.NewRoot(t)
	// xkcd reachable from the sandbox → an ungated sandbox-venue fetch (no operator
	// approval needed in this headless scenario; gating itself is tested elsewhere).
	cfg := sb.WriteConfig(t, root, `network:
  allowed_domains:
    - pattern: "xkcd.com"
      reachable_from: sandbox`)

	sb.Run(t, "bootstrap", "--config", cfg, "--transport", "sftp")

	fs := sb.DialFS(t, "sftp")
	ctx := context.Background()
	tree := protocol.Tree{Root: root}
	// A real project directory: package.json + index.js live together, and the relayed
	// node_modules lands beside them (local resolution, no NODE_PATH).
	proj := path.Join(root, "dashboard")
	if err := fs.Mkdir(ctx, proj); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	// 1. Fetch the comic through Popo and stage it as the app's input.
	comic := fetchComic(t, sb, fs, tree, root, cfg)
	rawComic, _ := json.Marshal(comic)
	if err := fs.WriteFile(ctx, path.Join(proj, "comic.json"), rawComic); err != nil {
		t.Fatalf("write comic.json: %v", err)
	}

	// 2. Provision Node, deploy the project manifest + source, and install the whole
	//    project's dependencies from its package.json via the manifest-driven relay.
	sb.Run(t, "install", "node", "24", "--config", cfg, "--transport", "sftp")
	if err := fs.WriteFile(ctx, path.Join(proj, "package.json"), appPkgJSON); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := fs.WriteFile(ctx, path.Join(proj, "index.js"), appJS); err != nil {
		t.Fatalf("write index.js: %v", err)
	}
	npmOut := string(sb.Run(t, "install", "npm", "--project", proj,
		"--node", "24", "--tier", "relay", "--config", cfg, "--transport", "sftp"))
	if !strings.Contains(npmOut, "installed blessed-contrib") || !strings.Contains(npmOut, "installed blessed ") {
		t.Fatalf("npm --project install output (want blessed + blessed-contrib):\n%s", npmOut)
	}

	// 3. Run the application from the project dir — node resolves ./node_modules with no
	//    NODE_PATH, exactly as a normal project would.
	nodeBin := nodeBinFrom(t, sb, root)
	out := sb.Sh(t, fmt.Sprintf("cd %s && %s %s %s",
		shq(proj), shq(nodeBin), shq("index.js"), shq("comic.json")))

	// 4. Assert the app ran headless, both libraries loaded and drove widgets, and it
	//    processed the fetched data.
	num := strconv.Itoa(int(comic["num"].(float64)))
	title, _ := comic["title"].(string)
	titleLen := strconv.Itoa(len(utf16.Encode([]rune(title)))) // JS string.length = UTF-16 units
	for _, want := range []string{"DASHBOARD_OK", num, title, titleLen} {
		if !strings.Contains(out, want) {
			t.Errorf("app output is missing %q:\n%s", want, out)
		}
	}
}

// nodeBinFrom finds the installed node interpreter's absolute path in the sandbox
// (runtimes/node/<ver>/bin/node), independent of the project's local node_modules.
func nodeBinFrom(t *testing.T, sb *harness.Sandbox, root string) string {
	t.Helper()
	out := strings.TrimSpace(sb.Sh(t, "ls -d "+shq(path.Join(root, "runtimes", "node"))+"/*/bin/node"))
	if out == "" || strings.Contains(out, "\n") {
		t.Fatalf("expected exactly one node bin under %s/runtimes/node, got %q", root, out)
	}
	return out
}

// fetchComic delivers a web.fetch for the xkcd JSON, services it with one serve
// cycle, and returns the parsed comic.
func fetchComic(t *testing.T, sb *harness.Sandbox, fs remotefs.FS, tree protocol.Tree, root, cfg string) map[string]any {
	t.Helper()
	ctx := context.Background()
	id := protocol.NewID()
	name := protocol.RequestName(id)
	data, _ := json.Marshal(protocol.Request{
		SchemaVersion: protocol.SchemaVersion, ID: id, Type: "web.fetch",
		CreatedAt: time.Now().UTC(), Params: json.RawMessage(`{"url":"https://xkcd.com/info.0.json"}`),
	})
	if err := protocol.Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
		t.Fatalf("deliver web.fetch: %v", err)
	}
	sb.Run(t, "serve", "--once", "--config", cfg, "--transport", "sftp")

	resp, err := protocol.ReadResponse(ctx, fs, tree, name)
	if err != nil {
		t.Fatalf("read web.fetch response: %v", err)
	}
	if resp.Status != protocol.StatusOK {
		t.Fatalf("web.fetch status = %q, error = %+v", resp.Status, resp.Error)
	}
	var r struct {
		Encoding   string `json:"encoding"`
		BodyInline string `json:"body_inline"`
		BodyBlob   string `json:"body_blob"`
	}
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("unmarshal fetch result: %v", err)
	}
	body := []byte(r.BodyInline)
	if len(body) == 0 && r.BodyBlob != "" { // large body: read the blob
		b, err := fs.ReadFile(ctx, path.Join(root, "protocol", r.BodyBlob))
		if err != nil {
			t.Fatalf("read body blob: %v", err)
		}
		body = b
	}
	if r.Encoding == "base64" {
		dec, err := base64.StdEncoding.DecodeString(string(body))
		if err != nil {
			t.Fatalf("decode base64 body: %v", err)
		}
		body = dec
	}
	var comic map[string]any
	if err := json.Unmarshal(body, &comic); err != nil {
		t.Fatalf("parse comic JSON: %v\nbody: %s", err, body)
	}
	if _, ok := comic["num"].(float64); !ok {
		t.Fatalf("fetched JSON missing numeric num: %v", comic)
	}
	return comic
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
