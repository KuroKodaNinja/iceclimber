// Package webfetch performs HTTP fetches over the sandbox's own egress (the
// "sandbox-exec" venue, plan §6): Popo runs curl — or busybox wget — inside the
// sandbox over the exec channel. It deliberately does NOT use Python (web.fetch
// is language-agnostic). The controller venue + egress gating live in phase 6b.
package webfetch

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/oklog/ulid/v2"
)

// inlineMax is the body size under which the body is returned inline (§4.4);
// larger bodies are stored as a content-addressed blob.
const inlineMax = 16 * 1024

// Request is a web.fetch request (plan §4.4).
type Request struct {
	URL     string
	Method  string // default GET
	Headers map[string]string
	Body    *string
}

// Result is a web.fetch result (plan §4.4).
type Result struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers,omitempty"`
	Venue      string            `json:"venue"`
	Encoding   string            `json:"encoding,omitempty"`
	BodyInline string            `json:"body_inline,omitempty"`
	BodyBlob   string            `json:"body_blob,omitempty"`
	BodySize   int               `json:"-"` // for the audit log
	BodySHA256 string            `json:"-"`
}

// Fetcher runs fetches inside one sandbox.
type Fetcher struct {
	runner remote.Runner
	fs     remotefs.FS
	root   string
	tool   string // cached detection: "curl" | "wget"
}

// New builds a Fetcher.
func New(runner remote.Runner, fs remotefs.FS, root string) *Fetcher {
	return &Fetcher{runner: runner, fs: fs, root: root}
}

// Fetch performs the request over the sandbox venue and returns the result.
func (f *Fetcher) Fetch(ctx context.Context, req Request) (Result, error) {
	method := strings.ToUpper(req.Method)
	if method == "" {
		method = "GET"
	}
	if err := checkSSRF(req.URL); err != nil {
		return Result{}, err
	}
	tool, err := f.detectTool(ctx)
	if err != nil {
		return Result{}, err
	}

	blobsDir := path.Join(f.root, "blobs")
	if err := f.fs.Mkdir(ctx, blobsDir); err != nil {
		return Result{}, fmt.Errorf("ensure blobs dir: %w", err)
	}
	bodyFile := path.Join(blobsDir, ".fetch-"+ulid.Make().String())
	defer func() { _ = f.fs.RemoveAll(ctx, bodyFile) }()

	cmd, stdin, err := buildCmd(tool, method, req, bodyFile)
	if err != nil {
		return Result{}, err
	}
	res, err := f.runner.Run(ctx, cmd, stdin)
	if err != nil {
		return Result{}, fmt.Errorf("run %s: %w", tool, err)
	}
	status, headers := parseMeta(tool, res)
	if status == 0 && res.ExitCode != 0 {
		return Result{}, fmt.Errorf("%s failed: %s", tool, lastLine(res.Stderr))
	}

	body, _ := f.fs.ReadFile(ctx, bodyFile) // missing/empty body file → nil
	enc, inline, blobName, sha := classifyBody(body)
	out := Result{
		StatusCode: status,
		Headers:    headers,
		Venue:      "sandbox-exec",
		BodySize:   len(body),
		BodySHA256: sha,
	}
	if blobName != "" {
		if err := f.fs.Rename(ctx, bodyFile, path.Join(blobsDir, blobName)); err != nil {
			return Result{}, fmt.Errorf("store blob: %w", err)
		}
		out.BodyBlob = "blobs/" + blobName
	} else {
		out.Encoding, out.BodyInline = enc, inline
	}
	return out, nil
}

// classifyBody decides how a body is returned: inline utf8, inline base64 (when
// not valid UTF-8), or a content-addressed blob (over inlineMax). It always
// returns the sha256 for the audit log.
func classifyBody(body []byte) (encoding, inline, blobName, sha string) {
	sum := sha256.Sum256(body)
	sha = hex.EncodeToString(sum[:])
	if len(body) < inlineMax {
		if utf8.Valid(body) {
			return "utf8", string(body), "", sha
		}
		return "base64", base64.StdEncoding.EncodeToString(body), "", sha
	}
	return "", "", sha, sha
}

func (f *Fetcher) detectTool(ctx context.Context) (string, error) {
	if f.tool != "" {
		return f.tool, nil
	}
	res, err := f.runner.Run(ctx, "command -v curl >/dev/null 2>&1 && echo curl || { command -v wget >/dev/null 2>&1 && echo wget; }", nil)
	if err != nil {
		return "", err
	}
	switch t := strings.TrimSpace(string(res.Stdout)); t {
	case "curl", "wget":
		f.tool = t
		return t, nil
	default:
		return "", fmt.Errorf("sandbox has neither curl nor wget for web.fetch")
	}
}

// buildCmd builds the shell command (quoted) and the stdin reader (for POST
// bodies via curl).
func buildCmd(tool, method string, req Request, bodyFile string) (string, io.Reader, error) {
	switch tool {
	case "curl":
		args := []string{
			"curl", "-sS", "-o", remote.ShellQuote(bodyFile),
			"-w", remote.ShellQuote("%{http_code}"), "-X", remote.ShellQuote(method),
		}
		for _, k := range sortedKeys(req.Headers) {
			args = append(args, "-H", remote.ShellQuote(k+": "+req.Headers[k]))
		}
		var stdin io.Reader
		if req.Body != nil {
			args = append(args, "--data-binary", "@-")
			stdin = strings.NewReader(*req.Body)
		}
		args = append(args, remote.ShellQuote(req.URL))
		return strings.Join(args, " "), stdin, nil

	case "wget":
		if method != "GET" && method != "POST" {
			return "", nil, fmt.Errorf("method %s needs curl (sandbox has only wget)", method)
		}
		args := []string{"wget", "-q", "-S", "-O", remote.ShellQuote(bodyFile)}
		for _, k := range sortedKeys(req.Headers) {
			args = append(args, "--header", remote.ShellQuote(k+": "+req.Headers[k]))
		}
		if method == "POST" {
			body := ""
			if req.Body != nil {
				body = *req.Body
			}
			args = append(args, "--post-data", remote.ShellQuote(body))
		}
		args = append(args, remote.ShellQuote(req.URL))
		return strings.Join(args, " "), nil, nil
	}
	return "", nil, fmt.Errorf("unknown fetch tool %q", tool)
}

// parseMeta extracts the status code (and, for wget, response headers). curl
// reports the code on stdout via -w; busybox wget reports the status line and
// headers on stderr via -S.
func parseMeta(tool string, res remote.Result) (int, map[string]string) {
	if tool == "curl" {
		code, _ := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
		return code, nil
	}
	status := 0
	headers := map[string]string{}
	for _, line := range strings.Split(string(res.Stderr), "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "HTTP/") {
			if fields := strings.Fields(l); len(fields) >= 2 {
				if c, err := strconv.Atoi(fields[1]); err == nil {
					status = c // last status wins (after redirects)
				}
			}
			headers = map[string]string{} // reset per response
		} else if i := strings.IndexByte(l, ':'); i > 0 && status != 0 {
			headers[strings.TrimSpace(l[:i])] = strings.TrimSpace(l[i+1:])
		}
	}
	if len(headers) == 0 {
		headers = nil
	}
	return status, headers
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
