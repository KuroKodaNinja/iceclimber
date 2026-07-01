package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/wire"
)

// buildParams turns a verb's CLI args into its params object. Per-verb arg shapes are
// the only verb-specific knowledge here; the rest of popo is generic.
func buildParams(verb string, args []string) (any, error) {
	switch verb {
	case "ping":
		return nil, nil

	case "python.install", "node.install", "java.install":
		if len(args) != 1 {
			return nil, fmt.Errorf("want exactly one version")
		}
		return map[string]any{"version": args[0]}, nil

	case "pip.install":
		ver, rest, err := requireFlag(args, "--python")
		if err != nil {
			return nil, err
		}
		pkgs, err := parsePkgs(rest, "==")
		if err != nil {
			return nil, err
		}
		return map[string]any{"python_version": ver, "packages": pkgs}, nil

	case "conda.install":
		ver, rest, err := requireFlag(args, "--python")
		if err != nil {
			return nil, err
		}
		// Collect repeatable channel flags into extra_args (-c conda-forge …).
		var extra []string
		for {
			ch, r2, ok := takeFlag(rest, "-c")
			if !ok {
				ch, r2, ok = takeFlag(rest, "--channel")
			}
			if !ok {
				break
			}
			if ch == "" {
				return nil, fmt.Errorf("-c/--channel needs a value")
			}
			rest, extra = r2, append(extra, "-c", ch)
		}
		// --offline selects the air-gapped relay tier (the controller resolves + downloads
		// + pushes an offline channel). Bare flag, forwarded via extra_args — the handler's
		// resolveTier keys on it. Without it, conda.install uses the sandbox's own channel.
		if r2, ok := takeBoolFlag(rest, "--offline"); ok {
			rest, extra = r2, append(extra, "--offline")
		}
		pkgs, err := parsePkgs(rest, "=") // conda match-spec uses a single '='
		if err != nil {
			return nil, err
		}
		p := map[string]any{"python_version": ver, "packages": pkgs}
		if len(extra) > 0 {
			p["extra_args"] = extra
		}
		return p, nil

	case "npm.install":
		ver, rest, err := requireFlag(args, "--node")
		if err != nil {
			return nil, err
		}
		// --project <dir>: install a whole package.json in the sandbox (npm install/ci)
		// instead of named packages.
		if proj, r2, ok := takeFlag(rest, "--project"); ok {
			if proj == "" {
				return nil, fmt.Errorf("--project needs a directory")
			}
			if len(r2) > 0 {
				return nil, fmt.Errorf("--project installs a package.json; don't also pass package names")
			}
			return map[string]any{"node_version": ver, "project": proj}, nil
		}
		pkgs, err := parsePkgs(rest, "@")
		if err != nil {
			return nil, err
		}
		return map[string]any{"node_version": ver, "packages": pkgs}, nil

	case "maven.install":
		ver, rest, err := requireFlag(args, "--java")
		if err != nil {
			return nil, err
		}
		if len(rest) == 0 {
			return nil, fmt.Errorf("want at least one group:artifact:version coordinate")
		}
		pkgs := make([]map[string]any, 0, len(rest))
		for _, a := range rest {
			p := strings.Split(a, ":")
			if len(p) != 3 || p[0] == "" || p[1] == "" || p[2] == "" {
				return nil, fmt.Errorf("invalid coordinate %q (want group:artifact:version)", a)
			}
			pkgs = append(pkgs, map[string]any{"name": p[0] + ":" + p[1], "version": p[2]})
		}
		return map[string]any{"java_version": ver, "packages": pkgs}, nil

	case "web.fetch":
		method, args, _ := takeFlag(args, "--method")
		body, args, hasBody := takeFlag(args, "--body")
		headers := map[string]any{}
		for {
			h, rest, ok := takeFlag(args, "--header")
			if !ok {
				break
			}
			args = rest
			k, v, found := strings.Cut(h, ":")
			if !found {
				return nil, fmt.Errorf("--header wants K:V, got %q", h)
			}
			headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		if len(args) != 1 {
			return nil, fmt.Errorf("want exactly one URL")
		}
		p := map[string]any{"url": args[0]}
		if method != "" {
			p["method"] = method
		}
		if len(headers) > 0 {
			p["headers"] = headers
		}
		if hasBody {
			p["body"] = body
		}
		return p, nil
	}
	return nil, fmt.Errorf("unknown verb")
}

// parsePkgs turns "name" / "name<sep>version" specs into package objects. The leading
// '@' of a scoped npm name is not a version separator, so we split on the last sep.
func parsePkgs(args []string, sep string) ([]map[string]any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("want at least one package")
	}
	out := make([]map[string]any, 0, len(args))
	for _, a := range args {
		name, version := a, ""
		if i := strings.LastIndex(a, sep); i > 0 {
			name, version = a[:i], a[i+len(sep):]
		}
		p := map[string]any{"name": name}
		if version != "" {
			p["version"] = version
		}
		out = append(out, p)
	}
	return out, nil
}

// takeFlag removes the first "--flag value" pair, returning the value and the
// remaining args. found is false if the flag is absent.
func takeFlag(args []string, flag string) (val string, rest []string, found bool) {
	for i := 0; i < len(args); i++ {
		if args[i] == flag {
			if i+1 >= len(args) {
				return "", args, true // present but no value → requireFlag/caller errors
			}
			rest = append(append([]string{}, args[:i]...), args[i+2:]...)
			return args[i+1], rest, true
		}
	}
	return "", args, false
}

// takeBoolFlag removes a bare (valueless) flag if present, returning the remaining args.
func takeBoolFlag(args []string, flag string) (rest []string, found bool) {
	for i, a := range args {
		if a == flag {
			return append(append([]string{}, args[:i]...), args[i+1:]...), true
		}
	}
	return args, false
}

func requireFlag(args []string, flag string) (string, []string, error) {
	val, rest, found := takeFlag(args, flag)
	if !found {
		return "", nil, fmt.Errorf("missing required %s", flag)
	}
	if val == "" {
		return "", nil, fmt.Errorf("%s needs a value", flag)
	}
	return val, rest, nil
}

// report prints a clean result and returns the process exit code (0 ok, 1 error,
// 2 needs-clarification). Output is read from the result generically, so a new
// result field shows up without code changes (and unknown shapes pretty-print).
func report(verb, root string, r wire.Response) int {
	switch r.Status {
	case wire.StatusNeedsClarification:
		q := "(no question provided)"
		if r.Clarification != nil {
			q = r.Clarification.Question
		}
		fmt.Fprintf(os.Stderr, "needs approval: %s\nRelay this to the operator; once approved, run the command again.\n", q)
		return 2
	case wire.StatusError:
		if r.Error != nil {
			fmt.Fprintf(os.Stderr, "error [%s]: %s\n", r.Error.Code, r.Error.Message)
		} else {
			fmt.Fprintln(os.Stderr, "error (no detail)")
		}
		return 1
	case wire.StatusOK:
		printResult(os.Stdout, verb, root, r.Result)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unexpected status %q\n", r.Status)
		return 1
	}
}

func printResult(w io.Writer, verb, root string, result json.RawMessage) {
	var m map[string]any
	if len(result) == 0 || json.Unmarshal(result, &m) != nil {
		fmt.Fprintln(w, strings.TrimSpace(string(result)))
		return
	}
	switch {
	case m["path"] != nil: // runtime install
		line := fmt.Sprintf("✓ %s %s → %s", verb, str(m["version"]), str(m["path"]))
		if b, _ := m["already_installed"].(bool); b {
			line += " (already installed)"
		}
		fmt.Fprintln(w, line)
	case m["installed"] != nil || m["failed"] != nil: // package install
		for _, it := range arr(m["installed"]) {
			fmt.Fprintf(w, "✓ %s %s (%s)\n", str(it["name"]), str(it["version"]), str(it["tier"]))
		}
		for _, it := range arr(m["failed"]) {
			fmt.Fprintf(w, "✗ %s %s: %s\n", str(it["name"]), str(it["version"]), str(it["error"]))
		}
		if np := str(m["node_path"]); np != "" {
			fmt.Fprintf(w, "NODE_PATH=%s\n", np)
		}
		if cp := str(m["classpath"]); cp != "" {
			fmt.Fprintf(w, "classpath=%s\n", cp)
		}
	case m["status_code"] != nil: // web.fetch
		fmt.Fprintf(w, "HTTP %s (%s)\n", num(m["status_code"]), str(m["venue"]))
		if bi := str(m["body_inline"]); bi != "" {
			fmt.Fprintln(w, bi)
		} else if bb := str(m["body_blob"]); bb != "" {
			fmt.Fprintf(w, "body: %s/%s\n", root, bb)
		}
	case m["popo_version"] != nil: // ping
		fmt.Fprintf(w, "bridge up (Popo %s)\n", str(m["popo_version"]))
	default:
		b, _ := json.MarshalIndent(m, "", "  ")
		fmt.Fprintln(w, string(b))
	}
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func num(v any) string {
	if f, ok := v.(float64); ok {
		return fmt.Sprintf("%d", int(f))
	}
	return str(v)
}

func arr(v any) []map[string]any {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func helpText(verb string) string {
	if verb != "" {
		if usage, ok := verbs[verb]; ok {
			return usage + "\n"
		}
		return fmt.Sprintf("unknown verb %q\n", verb)
	}
	var b strings.Builder
	b.WriteString("popo — ask Popo (the controller) to provision things in this sandbox.\n\n")
	b.WriteString("Usage: popo <verb> [args]   (blocks until Popo responds; prints the result)\n")
	b.WriteString("Exit: 0 ok · 1 error · 2 needs operator approval (relay the message, then retry)\n\n")
	b.WriteString("Verbs:\n")
	names := make([]string, 0, len(verbs))
	for n := range verbs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b.WriteString("  " + verbs[n] + "\n")
	}
	b.WriteString("\nRun an installed runtime by the absolute path popo prints (e.g. <path> -c \"print(1)\").\n")
	return b.String()
}
