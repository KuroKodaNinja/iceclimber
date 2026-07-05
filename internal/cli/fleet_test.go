package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
)

func members(ids ...string) []*config.Config {
	var m []*config.Config
	for _, id := range ids {
		m = append(m, &config.Config{SandboxID: id})
	}
	return m
}

// fanOut keeps going past a member failure; the summary reports ok/failed and errors overall.
func TestFanOut_KeepsGoingAndSummarizes(t *testing.T) {
	var out bytes.Buffer
	res := fanOut(context.Background(), &out, members("a", "b", "c"), func(_ context.Context, c *config.Config, w io.Writer) error {
		fmt.Fprintf(w, "working on %s\n", c.SandboxID)
		if c.SandboxID == "b" {
			return fmt.Errorf("boom")
		}
		return nil
	})
	// every member ran (b's failure didn't abort a/c)
	byID := map[string]error{}
	for _, r := range res {
		byID[r.ID] = r.Err
	}
	if byID["a"] != nil || byID["c"] != nil || byID["b"] == nil {
		t.Fatalf("keep-going failed: %+v", byID)
	}
	// output is [box]-prefixed
	if !strings.Contains(out.String(), "[a] working on a") || !strings.Contains(out.String(), "[b] working on b") {
		t.Fatalf("output not prefixed:\n%s", out.String())
	}
	var sum bytes.Buffer
	err := summarizeFanOut(&sum, "install", res)
	if err == nil {
		t.Fatal("summary must return an error when a member failed")
	}
	if !strings.Contains(sum.String(), "2 ok, 1 failed") || !strings.Contains(sum.String(), "b: boom") {
		t.Fatalf("summary wrong:\n%s", sum.String())
	}
}

// concurrency is bounded to fanOutConcurrency simultaneous ops.
func TestFanOut_BoundedConcurrency(t *testing.T) {
	var cur, max int32
	var mu sync.Mutex
	block := make(chan struct{})
	m := members("a", "b", "c", "d", "e", "f", "g", "h")
	go func() {
		res := fanOut(context.Background(), &bytes.Buffer{}, m, func(_ context.Context, _ *config.Config, _ io.Writer) error {
			n := atomic.AddInt32(&cur, 1)
			mu.Lock()
			if n > max {
				max = n
			}
			mu.Unlock()
			<-block
			atomic.AddInt32(&cur, -1)
			return nil
		})
		_ = res
	}()
	// let goroutines pile up against the semaphore, then release
	for i := 0; i < 50; i++ {
		if atomic.LoadInt32(&cur) >= int32(fanOutConcurrency) {
			break
		}
	}
	close(block)
	if max > int32(fanOutConcurrency) {
		t.Fatalf("max concurrent = %d, want <= %d", max, fanOutConcurrency)
	}
}

func TestPrefixWriter_BuffersPartialLines(t *testing.T) {
	var out bytes.Buffer
	var mu sync.Mutex
	p := &prefixWriter{mu: &mu, w: &out, prefix: "[x] "}
	fmt.Fprint(p, "hel")
	fmt.Fprint(p, "lo\nwor")
	if out.String() != "[x] hello\n" {
		t.Fatalf("partial line should buffer: %q", out.String())
	}
	p.flush()
	if out.String() != "[x] hello\n[x] wor\n" {
		t.Fatalf("flush should emit the tail: %q", out.String())
	}
}
