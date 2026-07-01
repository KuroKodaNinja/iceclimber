//go:build scenario

package harness

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
)

const (
	sandboxName      = "iceclimber-sandbox"       // musl (Alpine)
	glibcSandboxName = "iceclimber-sandbox-glibc" // glibc (Ubuntu) — for conda/manylinux scenarios
)

// Sandbox is a discovered, ready-to-drive functional sandbox plus the built binary.
type Sandbox struct {
	name string // the Lima instance name (musl or glibc)
	conn conn
	Bin  string // path to the freshly built iceclimber binary
}

type conn struct {
	Host         string
	Port         int
	User         string
	IdentityFile string
	KnownHosts   string
}

// Each sandbox is discovered and the binary built exactly once per scenario test
// binary (the host key is stable for the VM's lifetime). The binary build is shared
// across both boxes.
var (
	musl      sandboxOnce
	glibc     sandboxOnce
	binOnce   sync.Once
	sharedBin string
	binErr    error
)

type sandboxOnce struct {
	once   sync.Once
	sb     *Sandbox
	skip   string
	setErr error
}

// Require returns a ready musl (Alpine) Sandbox, or skips with an actionable message
// when the Lima sandbox isn't running. Build it with `make sandbox-up`.
func Require(t *testing.T) *Sandbox {
	return requireNamed(t, &musl, sandboxName, "make sandbox-up")
}

// RequireGlibc returns a ready glibc (Ubuntu) Sandbox — used by conda/manylinux
// scenarios. Skips when the box isn't running (`make sandbox-glibc-up`).
func RequireGlibc(t *testing.T) *Sandbox {
	return requireNamed(t, &glibc, glibcSandboxName, "make sandbox-glibc-up")
}

func requireNamed(t *testing.T, s *sandboxOnce, name, upHint string) *Sandbox {
	t.Helper()
	s.once.Do(func() { s.sb, s.skip, s.setErr = setup(name, upHint) })
	if s.setErr != nil {
		t.Fatalf("scenario setup: %v", s.setErr)
	}
	if s.skip != "" {
		t.Skip(s.skip)
	}
	return s.sb
}

func setup(name, upHint string) (*Sandbox, string, error) {
	c, sk, err := discoverNamed(name, upHint)
	if err != nil || sk != "" {
		return nil, sk, err
	}
	binOnce.Do(func() { sharedBin, binErr = buildBinary() })
	if binErr != nil {
		return nil, "", binErr
	}
	return &Sandbox{name: name, conn: c, Bin: sharedBin}, "", nil
}

// NewRoot returns a fresh, isolated sandbox install root, removed at test end.
func (s *Sandbox) NewRoot(t *testing.T) string {
	t.Helper()
	root := "/tmp/iceclimber-scn-" + protocol.NewID()
	t.Cleanup(func() {
		_ = exec.Command("limactl", "shell", s.name, "--", "rm", "-rf", root).Run()
	})
	return root
}

// WriteConfig writes an iceclimber.yaml pinned to root, plus any extra YAML (e.g.
// a network.allowed_domains block), into the test's temp dir.
func (s *Sandbox) WriteConfig(t *testing.T, root, extraYAML string) string {
	t.Helper()
	content := fmt.Sprintf(`sandbox_id: %s
ssh:
  host: %s
  port: %d
  user: %s
  identity_file: %s
  known_hosts: %s
remote_root: %s
`, s.name, s.conn.Host, s.conn.Port, s.conn.User, s.conn.IdentityFile, s.conn.KnownHosts, root)
	if extraYAML != "" {
		content += strings.TrimRight(extraYAML, "\n") + "\n"
	}
	p := filepath.Join(t.TempDir(), "iceclimber.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Run executes the built binary, failing the test (with stderr) on a non-zero exit.
func (s *Sandbox) Run(t *testing.T, args ...string) []byte {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(s.Bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("iceclimber %v: %v\nstderr: %s", args, err, stderr.String())
	}
	return stdout.Bytes()
}

// Sh runs a /bin/sh script inside the sandbox VM.
func (s *Sandbox) Sh(t *testing.T, script string) string {
	t.Helper()
	out, err := exec.Command("limactl", "shell", s.name, "--", "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox sh %q: %v\n%s", script, err, out)
	}
	return string(out)
}

// Fetch delivers a web.fetch for url, services it with one serve cycle, and returns
// the decoded response body. The config must allow the sandbox to reach the host
// (network.allowed_domains … reachable_from: sandbox). Shared plumbing so each
// language scenario doesn't re-implement the fetch.
func (s *Sandbox) Fetch(t *testing.T, fs remotefs.FS, cfg, root, url string) []byte {
	t.Helper()
	ctx := context.Background()
	tree := protocol.Tree{Root: root}
	id := protocol.NewID()
	name := protocol.RequestName(id)
	data, _ := json.Marshal(protocol.Request{
		SchemaVersion: protocol.SchemaVersion, ID: id, Type: "web.fetch",
		CreatedAt: time.Now().UTC(), Params: json.RawMessage(fmt.Sprintf(`{"url":%q}`, url)),
	})
	if err := protocol.Deliver(ctx, fs, tree.Outbox(), name, data); err != nil {
		t.Fatalf("deliver web.fetch: %v", err)
	}
	s.Run(t, "serve", "--once", "--config", cfg, "--transport", "sftp")

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
	if len(body) == 0 && r.BodyBlob != "" {
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
	return body
}

// DialFS opens a RemoteFS over the sandbox (transport "sftp" or "exec"), closed at
// test end.
func (s *Sandbox) DialFS(t *testing.T, transport string) remotefs.FS {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := remote.Dial(ctx, remote.DialConfig{
		Host: s.conn.Host, Port: s.conn.Port, User: s.conn.User,
		IdentityFile: s.conn.IdentityFile, KnownHosts: s.conn.KnownHosts,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if transport == "exec" {
		t.Cleanup(func() { _ = r.Close() })
		return remotefs.NewExecFS(r)
	}
	sc, err := r.NewSFTP()
	if err != nil {
		_ = r.Close()
		t.Fatalf("NewSFTP: %v", err)
	}
	t.Cleanup(func() { _ = sc.Close(); _ = r.Close() })
	return remotefs.NewSFTPFS(sc)
}

// --- discovery + build (mirrors test/functional/lima_test.go) ---

type limaJSON struct {
	Status       string `json:"status"`
	Dir          string `json:"dir"`
	SSHLocalPort int    `json:"sshLocalPort"`
}

func discoverNamed(name, upHint string) (conn, string, error) {
	if _, err := exec.LookPath("limactl"); err != nil {
		return conn{}, "limactl not found; install Lima and run `" + upHint + "`", nil
	}
	inst, err := limaInstance(name)
	if err != nil {
		return conn{}, fmt.Sprintf("sandbox %q not found (%v); run `%s`", name, err, upHint), nil
	}
	if inst.Status != "Running" {
		return conn{}, fmt.Sprintf("sandbox %q is %q, not Running; run `%s`", name, inst.Status, upHint), nil
	}
	if inst.SSHLocalPort == 0 {
		return conn{}, fmt.Sprintf("sandbox %q has no ssh port yet; is it still booting?", name), nil
	}
	host := sshConfigField(inst.Dir, "Hostname")
	if host == "" {
		host = "127.0.0.1"
	}
	usr := sshConfigField(inst.Dir, "User")
	if usr == "" {
		if u, err := user.Current(); err == nil {
			usr = u.Username
		}
	}
	identity := sshConfigField(inst.Dir, "IdentityFile")
	if identity == "" {
		home, _ := os.UserHomeDir()
		identity = filepath.Join(home, ".lima", "_config", "user")
	}
	known, err := keyscanToFile(host, inst.SSHLocalPort)
	if err != nil {
		return conn{}, "", err
	}
	return conn{Host: host, Port: inst.SSHLocalPort, User: usr, IdentityFile: identity, KnownHosts: known}, "", nil
}

func limaInstance(name string) (limaJSON, error) {
	var out bytes.Buffer
	cmd := exec.Command("limactl", "list", "--json", name)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return limaJSON{}, fmt.Errorf("limactl list: %w", err)
	}
	var inst limaJSON
	if err := json.NewDecoder(&out).Decode(&inst); err != nil {
		return limaJSON{}, fmt.Errorf("instance not present")
	}
	return inst, nil
}

func sshConfigField(dir, key string) string {
	data, err := os.ReadFile(filepath.Join(dir, "ssh.config"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.EqualFold(fields[0], key) {
			return strings.Trim(fields[1], `"`)
		}
	}
	return ""
}

func keyscanToFile(host string, port int) (string, error) {
	f, err := os.CreateTemp("", "iceclimber-scn-known_hosts-*")
	if err != nil {
		return "", err
	}
	defer f.Close()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		var out bytes.Buffer
		cmd := exec.Command("ssh-keyscan", "-T", "10", "-p", strconv.Itoa(port), host)
		cmd.Stdout = &out
		if err := cmd.Run(); err == nil && out.Len() > 0 {
			if _, werr := f.Write(out.Bytes()); werr != nil {
				return "", werr
			}
			return f.Name(), nil
		}
		lastErr = fmt.Errorf("ssh-keyscan %s:%d failed", host, port)
		time.Sleep(500 * time.Millisecond)
	}
	return "", lastErr
}

func buildBinary() (string, error) {
	dir, err := os.MkdirTemp("", "iceclimber-scn-bin")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "iceclimber")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = repoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("build iceclimber: %v\n%s", err, out)
	}
	return bin, nil
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}
