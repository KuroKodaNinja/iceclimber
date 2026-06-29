package webfetch

import (
	"context"
	"io"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

// fakeRunner answers tool detection with "curl" and every fetch with HTTP 200.
type fakeRunner struct{}

func (fakeRunner) Run(_ context.Context, cmd string, _ io.Reader) (remote.Result, error) {
	if strings.Contains(cmd, "command -v curl") {
		return remote.Result{Stdout: []byte("curl\n")}, nil
	}
	return remote.Result{Stdout: []byte("200")}, nil // curl -w http_code
}
func (fakeRunner) Close() error { return nil }

// fakeFS records mkdirs/renames and serves a fixed body for any ReadFile, so Fetch
// classifies the body as a blob.
type fakeFS struct {
	body    []byte
	mkdirs  []string
	renames [][2]string
}

func (f *fakeFS) Mkdir(_ context.Context, p string) error          { f.mkdirs = append(f.mkdirs, p); return nil }
func (f *fakeFS) WriteFile(context.Context, string, []byte) error  { return nil }
func (f *fakeFS) ReadFile(context.Context, string) ([]byte, error) { return f.body, nil }
func (f *fakeFS) List(context.Context, string) ([]string, error)   { return nil, nil }
func (f *fakeFS) Rename(_ context.Context, from, to string) error {
	f.renames = append(f.renames, [2]string{from, to})
	return nil
}
func (f *fakeFS) Chmod(context.Context, string, os.FileMode) error { return nil }
func (f *fakeFS) Symlink(context.Context, string, string) error    { return nil }
func (f *fakeFS) RemoveAll(context.Context, string) error          { return nil }

// TestFetch_BlobLandsInCanonicalDir is the regression guard for the blob-path bug:
// a large body must be stored at the path NANA.md documents ($ROOT/protocol/blobs),
// and the published body_blob must resolve, against $ROOT, to that exact file.
func TestFetch_BlobLandsInCanonicalDir(t *testing.T) {
	const root = "/srv/ice"
	big := make([]byte, inlineMax+1) // > inlineMax → stored as a blob, not inline
	for i := range big {
		big[i] = 'a'
	}
	fs := &fakeFS{body: big}
	f := New(fakeRunner{}, fs, root)

	out, err := f.Fetch(context.Background(), Request{URL: "https://example.com"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if out.BodyBlob == "" {
		t.Fatal("expected a body_blob for a large body")
	}

	tree := protocol.Tree{Root: root}
	// body_blob is the $ROOT-relative reference NANA.md documents.
	if !strings.HasPrefix(out.BodyBlob, "protocol/blobs/") {
		t.Errorf("body_blob = %q, want it under protocol/blobs/ (NANA.md spec)", out.BodyBlob)
	}
	// The blob was renamed into the canonical dir, NOT $ROOT/blobs.
	if len(fs.renames) != 1 {
		t.Fatalf("expected one rename (stage→final), got %v", fs.renames)
	}
	dest := fs.renames[0][1]
	if !strings.HasPrefix(dest, tree.Blobs()+"/") {
		t.Errorf("blob stored at %q, want under %q", dest, tree.Blobs())
	}
	if strings.HasPrefix(dest, path.Join(root, "blobs")+"/") {
		t.Errorf("blob stored under $ROOT/blobs (the old bug): %q", dest)
	}
	// THE invariant: $ROOT/<body_blob> is exactly where the blob was written.
	if got := path.Join(root, out.BodyBlob); got != dest {
		t.Errorf("$ROOT/body_blob = %q but blob is at %q — the agent would look in the wrong place", got, dest)
	}
}
