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

// TestNodeApp exercises the whole Node stack end to end: web.fetch (through Popo)
// → node.install → npm.install (figlet + cli-table3, relay) → build + run a real
// program → assert it rendered the computed report.
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
	work := path.Join(root, "work")
	if err := fs.Mkdir(ctx, work); err != nil {
		t.Fatalf("mkdir work: %v", err)
	}

	// 1. Fetch the comic through Popo and stage it as the app's input.
	comic := fetchComic(t, sb, fs, tree, root, cfg)
	rawComic, _ := json.Marshal(comic)
	if err := fs.WriteFile(ctx, path.Join(work, "comic.json"), rawComic); err != nil {
		t.Fatalf("write comic.json: %v", err)
	}

	// 2. Provision Node + the two npm packages (via relay).
	sb.Run(t, "install", "node", "24", "--config", cfg, "--transport", "sftp")
	npmOut := string(sb.Run(t, "install", "npm", "figlet", "cli-table3",
		"--node", "24", "--tier", "relay", "--config", cfg, "--transport", "sftp"))
	if !strings.Contains(npmOut, "installed figlet") || !strings.Contains(npmOut, "installed cli-table3") {
		t.Fatalf("npm install output:\n%s", npmOut)
	}
	nodePath := nodePathFrom(t, npmOut)

	// 3. Deploy + run the application.
	if err := fs.WriteFile(ctx, path.Join(work, "index.js"), appJS); err != nil {
		t.Fatalf("write index.js: %v", err)
	}
	nodeBin := strings.TrimSuffix(nodePath, "/lib/node_modules") + "/bin/node"
	out := sb.Sh(t, fmt.Sprintf("NODE_PATH=%s %s %s %s",
		shq(nodePath), shq(nodeBin), shq(path.Join(work, "index.js")), shq(path.Join(work, "comic.json"))))

	// 4. Assert the rendered report carries the computed values (the program ran,
	//    both libraries loaded, and it processed the fetched data).
	num := strconv.Itoa(int(comic["num"].(float64)))
	title, _ := comic["title"].(string)
	titleLen := strconv.Itoa(len(utf16.Encode([]rune(title)))) // JS string.length = UTF-16 units
	for _, want := range []string{num, title, titleLen} {
		if !strings.Contains(out, want) {
			t.Errorf("app output is missing %q:\n%s", want, out)
		}
	}
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

func nodePathFrom(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "NODE_PATH="); i >= 0 {
			return strings.TrimSpace(line[i+len("NODE_PATH="):])
		}
	}
	t.Fatalf("no NODE_PATH in npm output:\n%s", out)
	return ""
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
