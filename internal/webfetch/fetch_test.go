package webfetch

import (
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

func TestCheckSSRF(t *testing.T) {
	blocked := []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1/",
		"https://[::1]/",
		"http://0.0.0.0/",
	}
	for _, u := range blocked {
		if err := checkSSRF(u); err == nil {
			t.Errorf("checkSSRF(%q) = nil, want blocked", u)
		}
	}
	allowed := []string{
		"https://example.com/x",
		"http://1.1.1.1/",
		"http://10.0.0.5/",        // private is the legit internal net for the sandbox venue
		"https://mirror.internal", // hostname
	}
	for _, u := range allowed {
		if err := checkSSRF(u); err != nil {
			t.Errorf("checkSSRF(%q) = %v, want allowed", u, err)
		}
	}
	if err := checkSSRF("ftp://example.com/x"); err == nil {
		t.Error("non-http scheme should be rejected")
	}
}

func TestBuildCmd_Curl(t *testing.T) {
	body := `{"a":1}`
	cmd, stdin, err := buildCmd("curl", "POST", Request{
		URL:     "https://api.example.com/v1",
		Headers: map[string]string{"Authorization": "Bearer x"},
		Body:    &body,
	}, "/r/blobs/.fetch-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"curl -sS", "-o '/r/blobs/.fetch-1'", "-w '%{http_code}'", "-X 'POST'",
		"-H 'Authorization: Bearer x'", "--data-binary @-", "'https://api.example.com/v1'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("curl cmd missing %q in:\n%s", want, cmd)
		}
	}
	if stdin == nil {
		t.Error("POST body should produce a stdin reader")
	}
}

func TestBuildCmd_Wget(t *testing.T) {
	cmd, _, err := buildCmd("wget", "GET", Request{
		URL:     "https://example.com",
		Headers: map[string]string{"User-Agent": "ic/1"},
	}, "/r/blobs/.f")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"wget -q -S", "-O '/r/blobs/.f'", "--header 'User-Agent: ic/1'", "'https://example.com'"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("wget cmd missing %q in:\n%s", want, cmd)
		}
	}
	// A method busybox wget can't do must error.
	if _, _, err := buildCmd("wget", "DELETE", Request{URL: "https://x"}, "/b"); err == nil {
		t.Error("wget DELETE should require curl")
	}
}

func TestParseMeta(t *testing.T) {
	// curl: status on stdout.
	if code, _ := parseMeta("curl", remote.Result{Stdout: []byte("200\n")}); code != 200 {
		t.Errorf("curl status = %d, want 200", code)
	}
	// wget -S: status + headers on stderr, last status wins after a redirect.
	stderr := "  HTTP/1.1 301 Moved Permanently\n  Location: https://example.com/\n" +
		"  HTTP/1.1 200 OK\n  Content-Type: text/html\n  Content-Length: 42\n"
	code, headers := parseMeta("wget", remote.Result{Stderr: []byte(stderr)})
	if code != 200 {
		t.Errorf("wget status = %d, want 200", code)
	}
	if headers["Content-Type"] != "text/html" {
		t.Errorf("wget headers = %v, want Content-Type text/html", headers)
	}
	if _, ok := headers["Location"]; ok {
		t.Error("headers from the pre-redirect response should be reset")
	}
}

func TestClassifyBody(t *testing.T) {
	enc, inline, blob, sha := classifyBody([]byte("hello"))
	if enc != "utf8" || inline != "hello" || blob != "" || sha == "" {
		t.Errorf("utf8: %q %q %q", enc, inline, blob)
	}
	if enc, inline, blob, _ := classifyBody([]byte{0xff, 0xfe}); enc != "base64" || inline == "" || blob != "" {
		t.Errorf("binary: %q %q %q", enc, inline, blob)
	}
	big := make([]byte, inlineMax+1)
	if enc, inline, blob, sha := classifyBody(big); blob == "" || sha != blob || enc != "" || inline != "" {
		t.Errorf("blob: enc=%q inline=%q blob=%q sha=%q", enc, inline, blob, sha)
	}
}
