package rust

import (
	"context"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// fakeFS is a minimal remotefs.FS: only List is exercised by Locate; a missing dir returns
// fs.ErrNotExist to match the real transports' contract.
type fakeFS struct {
	dirs map[string][]string
}

func (f fakeFS) List(_ context.Context, dir string) ([]string, error) {
	if names, ok := f.dirs[dir]; ok {
		return names, nil
	}
	return nil, fs.ErrNotExist
}

func (fakeFS) Mkdir(context.Context, string) error                            { return nil }
func (fakeFS) WriteFile(context.Context, string, []byte) error                { return nil }
func (fakeFS) ReadFile(context.Context, string) ([]byte, error)               { return nil, nil }
func (fakeFS) ReadFrom(context.Context, string, int64) ([]byte, int64, error) { return nil, 0, nil }
func (fakeFS) Rename(context.Context, string, string) error                   { return nil }
func (fakeFS) Chmod(context.Context, string, os.FileMode) error               { return nil }
func (fakeFS) Symlink(context.Context, string, string) error                  { return nil }
func (fakeFS) RemoveAll(context.Context, string) error                        { return nil }

func TestLocate(t *testing.T) {
	root := "/opt/ice"
	dir := root + "/runtimes/rust"
	fsWith := func(names ...string) fakeFS { return fakeFS{dirs: map[string][]string{dir: names}} }

	// Highest patch wins among matching lines.
	got, err := Locate(context.Background(), fsWith(
		"1.83.0-x86_64-glibc", "1.83.2-x86_64-glibc", "1.82.0-x86_64-glibc",
	), root, "1.83", "x86_64", "glibc")
	if err != nil || got != dir+"/1.83.2-x86_64-glibc/bin/cargo" {
		t.Errorf("Locate(1.83) = %q, %v; want the 1.83.2 cargo", got, err)
	}

	// Empty version matches any (highest overall).
	got, err = Locate(context.Background(), fsWith("1.82.0-x86_64-glibc", "1.83.2-x86_64-glibc"), root, "", "x86_64", "glibc")
	if err != nil || !strings.HasSuffix(got, "1.83.2-x86_64-glibc/bin/cargo") {
		t.Errorf("Locate('') = %q, %v; want 1.83.2 cargo", got, err)
	}

	// Platform suffix must match (arch/libc filter).
	if _, err := Locate(context.Background(), fsWith("1.83.0-x86_64-glibc"), root, "", "aarch64", "glibc"); err == nil {
		t.Error("Locate wrong arch: want not-installed error")
	}
	if _, err := Locate(context.Background(), fsWith("1.83.0-x86_64-glibc"), root, "", "x86_64", "musl"); err == nil {
		t.Error("Locate wrong libc: want not-installed error")
	}

	// A requested line with no install is not-installed.
	if _, err := Locate(context.Background(), fsWith("1.82.0-x86_64-glibc"), root, "1.99", "x86_64", "glibc"); err == nil {
		t.Error("Locate missing line: want not-installed error")
	}

	// No runtimes/rust dir at all → not-installed (not a hard error).
	if _, err := Locate(context.Background(), fakeFS{dirs: map[string][]string{}}, root, "", "x86_64", "glibc"); err == nil {
		t.Error("Locate no dir: want not-installed error")
	}
}

func TestVersionMatches(t *testing.T) {
	cases := map[[2]string]bool{
		{"1.83.0", "1"}:      true,
		{"1.83.0", "1.83"}:   true,
		{"1.83.0", "1.83.0"}: true,
		{"1.83.0", "1.8"}:    false, // not a prefix-on-dot
		{"1.83.0", "2"}:      false,
		{"1.83.0", ""}:       true, // empty matches any
		{"1.83.0", "stable"}: true,
		{"1.83.0", "latest"}: true,
	}
	for in, want := range cases {
		if got := versionMatches(in[0], in[1]); got != want {
			t.Errorf("versionMatches(%q,%q) = %v, want %v", in[0], in[1], got, want)
		}
	}
}
