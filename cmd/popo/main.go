// Command popo is the in-sandbox client for the iceclimber bridge. The agent runs
// `popo <verb> …` instead of hand-crafting the maildir protocol: popo builds the
// request envelope, delivers it atomically, polls for the response (tracking Popo's
// heartbeat for liveness), and prints a clean result. It does only local file I/O —
// no network — and reuses internal/wire so its envelope and tree layout are the same
// ones the controller (Popo) speaks. Build static (CGO_ENABLED=0) so one binary per
// GOOS/GOARCH runs on musl and glibc alike.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/wire"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		verb := ""
		if len(args) > 1 {
			verb = args[1]
		}
		fmt.Print(helpText(verb))
		return
	}
	verb := args[0]
	rest := args[1:]

	// shellenv is local-only (no maildir round-trip): print an eval-able block so an
	// interactive sandbox shell can run popo/nana by name. `eval "$(./popo shellenv)"`.
	if verb == "shellenv" {
		root, err := resolveRoot()
		if err != nil {
			fmt.Fprintf(os.Stderr, "popo: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(shellEnvBlock(root))
		return
	}

	// collect is local-only: mark a response collected (inbox/new -> inbox/cur) so Popo's
	// GC can prune the pair. The normal request/await flow does this automatically; this
	// verb is for collecting a response read out-of-band.
	if verb == "collect" {
		root, err := resolveRoot()
		if err != nil {
			fmt.Fprintf(os.Stderr, "popo: %v\n", err)
			os.Exit(1)
		}
		if err := collectCmd(root, rest); err != nil {
			fmt.Fprintf(os.Stderr, "popo collect: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if _, ok := verbs[verb]; !ok {
		fmt.Fprintf(os.Stderr, "popo: unknown verb %q (run `popo help`)\n", verb)
		os.Exit(1)
	}
	params, err := buildParams(verb, rest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "popo %s: %v\n\n%s", verb, err, helpText(verb))
		os.Exit(1)
	}

	root, err := resolveRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "popo: %v\n", err)
		os.Exit(1)
	}

	resp, err := request(root, verb, params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "popo %s: %v\n", verb, err)
		os.Exit(1)
	}
	os.Exit(report(verb, root, resp))
}

// verbs is the catalog: name → one-line usage (also drives `popo help`).
var verbs = map[string]string{
	"ping":           "popo ping",
	"python.install": "popo python.install <minor>            e.g. 3.12",
	"pip.install":    "popo pip.install --python <minor> <pkg[==version]>...",
	"node.install":   "popo node.install <version-line>        e.g. 24",
	"npm.install":    "popo npm.install --node <line> <pkg[@version]>...",
	"java.install":   "popo java.install <feature>             e.g. 21",
	"maven.install":  "popo maven.install --java <feature> <group:artifact:version>...",
	"conda.install":  "popo conda.install --python <minor> [-c <channel>]... [--offline] <pkg[=version]>...",
	"web.fetch":      "popo web.fetch <url> [--method M] [--header K:V]... [--body STR]",
	"collect":        "popo collect <id>                       mark a response collected (usually automatic)",
	"shellenv":       "popo shellenv                           eval \"$(./popo shellenv)\" — popo/nana on PATH",
}

// shellEnvBlock renders the eval-able export block: ICECLIMBER_HOME + root on PATH
// (so popo/nana resolve by name). Minimal by design — no secrets.
func shellEnvBlock(root string) string {
	return fmt.Sprintf("export ICECLIMBER_HOME=%s\nexport PATH=%s:\"$PATH\"\n", shellQuote(root), shellQuote(root))
}

// shellQuote wraps s in single quotes, escaping embedded single quotes — a tiny
// local copy so popo stays a minimal static binary (no internal/remote import).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// resolveRoot finds $ICECLIMBER_HOME: ICECLIMBER_HOME if set, else the directory popo lives in
// ($ICECLIMBER_HOME/popo), so the agent never has to supply a path.
func resolveRoot() (string, error) {
	if r := os.Getenv("ICECLIMBER_HOME"); r != "" {
		return r, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot locate $ICECLIMBER_HOME (set ICECLIMBER_HOME): %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}

// request builds the envelope, delivers it atomically, and awaits the response.
func request(root, verb string, params any) (wire.Response, error) {
	tree := wire.Tree{Root: root}
	id := wire.NewID()
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return wire.Response{}, err
		}
		raw = b
	}
	env, err := json.Marshal(wire.Request{
		SchemaVersion: wire.SchemaVersion, ID: id, Type: verb,
		CreatedAt: time.Now().UTC(), Params: raw,
	})
	if err != nil {
		return wire.Response{}, err
	}

	name := wire.RequestName(id)
	tmp := filepath.Join(tree.Outbox().Tmp(), name)
	if err := os.WriteFile(tmp, env, 0o644); err != nil {
		return wire.Response{}, fmt.Errorf("write request (is the tree bootstrapped?): %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(tree.Outbox().New(), name)); err != nil {
		return wire.Response{}, fmt.Errorf("publish request: %w", err)
	}
	return await(tree, name)
}

// await polls inbox/new/<name> for the response, judging Popo's liveness by the
// heartbeat seq advancing — not the request's duration (installs can take minutes).
func await(tree wire.Tree, name string) (wire.Response, error) {
	respPath := filepath.Join(tree.Inbox().New(), name)
	backoff := []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}
	lastSeq, lastAdvance := "", time.Now()
	for i := 0; ; i++ {
		if data, err := os.ReadFile(respPath); err == nil {
			var r wire.Response
			if err := json.Unmarshal(data, &r); err != nil {
				return wire.Response{}, fmt.Errorf("parse response: %w", err)
			}
			_ = collect(tree, name) // best-effort: collected so Popo can prune; response still returned
			return r, nil
		}
		if seq := heartbeatSeq(tree); seq != "" && seq != lastSeq {
			lastSeq, lastAdvance = seq, time.Now()
		}
		if time.Since(lastAdvance) > 2*time.Minute {
			if lastSeq == "" {
				return wire.Response{}, fmt.Errorf("Popo isn't running (no heartbeat) — ask the operator to run `iceclimber serve`")
			}
			return wire.Response{}, fmt.Errorf("Popo appears down (heartbeat stalled) — ask the operator to run `iceclimber serve`")
		}
		d := backoff[len(backoff)-1]
		if i < len(backoff) {
			d = backoff[i]
		}
		time.Sleep(d)
	}
}

// collectCmd handles the `popo collect <id>` verb. It is IDEMPOTENT — an already-
// collected (or absent) id is success, not an error, because the request/await flow
// auto-collects and PROTOCOL.md also tells the agent to collect, so a double-collect is
// expected and harmless.
func collectCmd(root string, rest []string) error {
	if len(rest) != 1 {
		return fmt.Errorf("usage: popo collect <id>")
	}
	if err := collect(wire.Tree{Root: root}, wire.RequestName(rest[0])); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// collect marks a response collected by moving it inbox/new -> inbox/cur, so Popo's GC
// can prune the request/response pair and inbox/new reflects only uncollected mail. Done
// automatically by await; exposed explicitly via `popo collect <id>`.
func collect(tree wire.Tree, name string) error {
	return os.Rename(
		filepath.Join(tree.Inbox().New(), name),
		filepath.Join(tree.Inbox().Cur(), name),
	)
}

func heartbeatSeq(tree wire.Tree) string {
	data, err := os.ReadFile(tree.Heartbeat())
	if err != nil {
		return ""
	}
	if f := strings.Fields(string(data)); len(f) > 0 {
		return f[0]
	}
	return ""
}
