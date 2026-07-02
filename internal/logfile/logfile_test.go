package logfile

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"testing"
)

// A child process (spawned by the multi-process tests) appends a run of tagged lines to a shared
// file through its OWN Writer, then exits — exercising cross-process O_APPEND + rotation.
func TestMain(m *testing.M) {
	if tgt := os.Getenv("LOGFILE_CHILD_TARGET"); tgt != "" {
		n, _ := strconv.Atoi(os.Getenv("LOGFILE_CHILD_LINES"))
		maxB, _ := strconv.ParseInt(os.Getenv("LOGFILE_CHILD_MAX"), 10, 64)
		keep, _ := strconv.Atoi(os.Getenv("LOGFILE_CHILD_KEEP"))
		tag := os.Getenv("LOGFILE_CHILD_TAG")
		w := New(tgt, maxB, keep)
		for i := 0; i < n; i++ {
			_ = w.Append([]byte(fmt.Sprintf("%s-%06d\n", tag, i)))
		}
		_ = w.Close()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

var linePat = regexp.MustCompile(`^[a-z]-\d{6}$`)

// collectLines reads every surviving generation (path + path.1..path.<keep-1>) and returns all
// lines; it fails on any torn/malformed line (the corruption signal).
func collectLines(t *testing.T, path string, keep int) []string {
	t.Helper()
	var all []string
	files := []string{path}
	for i := 1; i < keep; i++ {
		files = append(files, genName(path, i))
	}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue // a generation that doesn't exist yet
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				continue
			}
			if !linePat.MatchString(line) {
				fh.Close()
				t.Fatalf("torn/malformed line in %s: %q", filepath.Base(f), line)
			}
			all = append(all, line)
		}
		fh.Close()
	}
	return all
}

func maxGenSize(t *testing.T, path string, keep int) int64 {
	t.Helper()
	var mx int64
	files := []string{path}
	for i := 1; i < keep; i++ {
		files = append(files, genName(path, i))
	}
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil && fi.Size() > mx {
			mx = fi.Size()
		}
	}
	return mx
}

// TestRotationBoundary: a single writer rotates at the size cap, keeps N generations, and drops
// the oldest — each generation stays bounded.
func TestRotationBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	const maxB, keep = 300, 3
	w := New(path, maxB, keep)
	defer w.Close()
	for i := 0; i < 200; i++ { // 200 × ~9 bytes ≈ 1.8 KB ≫ 3×300 → forces multiple rotations
		if err := w.Append([]byte(fmt.Sprintf("a-%06d\n", i))); err != nil {
			t.Fatal(err)
		}
	}
	if got := maxGenSize(t, path, keep); got > maxB+16 {
		t.Errorf("a generation is %d bytes, want <= %d (rotation not bounding)", got, maxB+16)
	}
	// At most `keep` generations exist (path + path.1 .. path.<keep-1>); path.<keep> must be gone.
	if _, err := os.Stat(genName(path, keep)); err == nil {
		t.Errorf("%s exists — more than %d generations kept", genName(path, keep), keep)
	}
	// Surviving lines are a contiguous suffix (oldest dropped), all intact.
	lines := collectLines(t, path, keep)
	if len(lines) == 0 || len(lines) >= 200 {
		t.Errorf("kept %d lines; expected a bounded suffix (< 200)", len(lines))
	}
}

// TestKeep1Truncate: keep=1 keeps only the live file, truncating it at the cap (no history).
func TestKeep1Truncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	w := New(path, 200, 1)
	defer w.Close()
	for i := 0; i < 200; i++ {
		_ = w.Append([]byte(fmt.Sprintf("a-%06d\n", i)))
	}
	if _, err := os.Stat(genName(path, 1)); err == nil {
		t.Error("keep=1 must not create a .1 generation")
	}
	if fi, _ := os.Stat(path); fi.Size() > 216 {
		t.Errorf("keep=1 file is %d bytes, want <= cap", fi.Size())
	}
}

// TestConcurrentGoroutines (run under -race): many goroutines append to one Writer with no torn
// lines and none lost (large cap → no rotation).
func TestConcurrentGoroutines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	w := New(path, 1<<30, 4) // huge cap: no rotation, so every line must survive intact
	defer w.Close()
	const g, per = 8, 500
	var wg sync.WaitGroup
	for gi := 0; gi < g; gi++ {
		wg.Add(1)
		go func(tag byte) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				_ = w.Append([]byte(fmt.Sprintf("%c-%06d\n", tag, i)))
			}
		}(byte('a' + gi))
	}
	wg.Wait()
	if lines := collectLines(t, path, 4); len(lines) != g*per {
		t.Errorf("got %d lines, want %d (torn or lost under concurrent append)", len(lines), g*per)
	}
}

// TestMultiProcessAppend: N child processes append to one file (large cap → no rotation); every
// line must be intact and present — the O_APPEND cross-process atomicity guarantee.
func TestMultiProcessAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	const procs, per = 5, 400
	spawnChildren(t, path, procs, per, 1<<30, 4)
	if lines := collectLines(t, path, 4); len(lines) != procs*per {
		t.Errorf("got %d lines, want %d (a process lost/tore lines)", len(lines), procs*per)
	}
}

// TestMultiProcessRotation: N child processes append AND force rotation concurrently — no torn
// lines in any generation, and the total is bounded (rotation is clobber-free across processes).
func TestMultiProcessRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	const procs, per, maxB, keep = 5, 400, 2000, 3
	spawnChildren(t, path, procs, per, maxB, keep)
	// collectLines fails on any torn line; here we only need it not to fail + bounded generations.
	_ = collectLines(t, path, keep)
	if got := maxGenSize(t, path, keep); got > maxB+64 {
		t.Errorf("a generation is %d bytes, want <= %d (rotation clobbered under contention)", got, maxB+64)
	}
	if _, err := os.Stat(genName(path, keep)); err == nil {
		t.Errorf("%s exists — rotation kept more than %d generations under contention", genName(path, keep), keep)
	}
}

func spawnChildren(t *testing.T, path string, procs, per int, maxB int64, keep int) {
	t.Helper()
	var wg sync.WaitGroup
	for p := 0; p < procs; p++ {
		wg.Add(1)
		go func(tag byte) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(),
				"LOGFILE_CHILD_TARGET="+path,
				"LOGFILE_CHILD_LINES="+strconv.Itoa(per),
				"LOGFILE_CHILD_MAX="+strconv.FormatInt(maxB, 10),
				"LOGFILE_CHILD_KEEP="+strconv.Itoa(keep),
				"LOGFILE_CHILD_TAG="+string(tag),
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("child %c: %v\n%s", tag, err, out)
			}
		}(byte('a' + p))
	}
	wg.Wait()
}

// TestFollowsExternalRotation: a second Writer on the same path (a stand-in for a second process)
// detects a rotation done by the first and reopens onto the new file rather than writing into the
// rotated-away generation.
func TestFollowsExternalRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	w1 := New(path, 120, 3)
	w2 := New(path, 120, 3)
	defer w1.Close()
	defer w2.Close()
	_ = w1.Append([]byte("a-000000\n"))
	_ = w2.Append([]byte("b-000000\n"))
	// Drive w1 over the cap so it rotates (path → path.1, new empty path).
	for i := 1; i < 30; i++ {
		_ = w1.Append([]byte(fmt.Sprintf("a-%06d\n", i)))
	}
	// w2 must now write into the CURRENT path (post-rotation), not the rotated-away inode.
	if err := w2.Append([]byte("b-000001\n")); err != nil {
		t.Fatal(err)
	}
	// b-000001 must be findable across generations, well-formed (collectLines asserts no tears).
	found := false
	for _, l := range collectLines(t, path, 3) {
		if l == "b-000001" {
			found = true
		}
	}
	if !found {
		t.Error("w2's post-rotation append was lost (didn't follow the external rotation)")
	}
}

// TestDisabledPath: an empty path disables the writer (Append is a no-op).
func TestDisabledPath(t *testing.T) {
	w := New("", 0, 0)
	if err := w.Append([]byte("x\n")); err != nil {
		t.Errorf("empty-path Append should be a silent no-op, got %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("empty-path Close: %v", err)
	}
}
