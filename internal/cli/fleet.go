package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/spf13/cobra"
)

// fanOutConcurrency bounds simultaneous member operations (§14.5): a small cap keeps SSH auth
// prompts serialized (via the caching prompter) and the artifact cache's per-asset flock makes
// the same download happen once no matter how many boxes want it.
const fanOutConcurrency = 4

// resolveFleet builds the fleet the fleet-aware commands target: an explicit `--config` fleet
// file (a `sandboxes:` map) wins; otherwise the per-sandbox registry IS the fleet — that's where
// `sandbox create`/`list` put boxes (decision #106). The single-file map stays as an operator-
// authored alternative.
func resolveFleet() (*config.Fleet, error) {
	// An explicit fleet file (default path or --config) → its `sandboxes:` map.
	if config.IsFleetFile(cfgFile) {
		return config.LoadFleet(cfgFile)
	}
	// An explicit non-default single file → a one-box fleet (respect the operator's pointer).
	if cfgFile != config.DefaultPath {
		c, err := config.Load(cfgFile, sandboxID)
		if err != nil {
			return nil, err
		}
		return config.NewFleet(cfgFile, []*config.Config{c})
	}
	// Default path: the per-sandbox registry IS the fleet (that's where `sandbox create`/`list`
	// put boxes); also fold in a classic `./iceclimber.yaml` single box if present. Deduped by id.
	seen := map[string]bool{}
	var members []*config.Config
	add := func(c *config.Config) {
		if c != nil && !seen[c.SandboxID] {
			seen[c.SandboxID] = true
			members = append(members, c)
		}
	}
	for _, e := range listSandboxes() {
		if e.Cfg != nil {
			add(e.Cfg)
		}
	}
	if c, err := config.Load(config.DefaultPath, ""); err == nil {
		add(c)
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("no sandboxes to target — create one (`iceclimber sandbox create`) or write a fleet config (`defaults:` + `sandboxes:`)")
	}
	return config.NewFleet("fleet", members)
}

// fleetTargetsRead resolves members for a READ-ONLY fleet command (status/logs/egress pending):
// a missing --sandbox defaults to ALL — that's what a fleet is for (§14.2).
func fleetTargetsRead() (*config.Fleet, []*config.Config, error) {
	f, err := resolveFleet()
	if err != nil {
		return nil, nil, err
	}
	sel := sandboxID
	if sel == "" {
		sel = "all"
	}
	m, err := f.Resolve(sel)
	return f, m, err
}

// fleetTargetsMutate resolves members for a MUTATING fan-out (install*/bootstrap/agent install):
// a missing --sandbox is an ERROR — a forgotten flag must never fan a mutation to N boxes (§14.2).
func fleetTargetsMutate() ([]*config.Config, error) {
	f, err := resolveFleet()
	if err != nil {
		return nil, err
	}
	if sandboxID == "" {
		return nil, fmt.Errorf("--sandbox is required to fan this out across the fleet (%s) — name a box, a glob, a list, or `--sandbox all`",
			strings.Join(f.IDs(), ", "))
	}
	return f.Resolve(sandboxID)
}

// fanResult is one member's fan-out outcome.
type fanResult struct {
	ID  string
	Err error
}

// fanOut runs op concurrently across members (bounded to fanOutConcurrency), keeping going past
// failures. Each op writes to a [box]-prefixed, line-buffered writer sharing out under a lock, so
// interleaved output stays attributable. Results are returned in member order. (§14.5)
func fanOut(ctx context.Context, out io.Writer, members []*config.Config, op func(context.Context, *config.Config, io.Writer) error) []fanResult {
	results := make([]fanResult, len(members))
	var mu sync.Mutex // guards `out` so prefixed lines from different members don't interleave mid-line
	sem := make(chan struct{}, fanOutConcurrency)
	var wg sync.WaitGroup
	for i, m := range members {
		i, m := i, m
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			pw := &prefixWriter{mu: &mu, w: out, prefix: "[" + m.SandboxID + "] "}
			err := op(ctx, m, pw)
			pw.flush()
			results[i] = fanResult{ID: m.SandboxID, Err: err}
		}()
	}
	wg.Wait()
	return results
}

// summarizeFanOut prints the ok/failed summary and returns a non-nil error when ANY member
// failed, so the command exits non-zero (§14.5).
func summarizeFanOut(out io.Writer, action string, results []fanResult) error {
	var ok, failed []string
	for _, r := range results {
		if r.Err != nil {
			failed = append(failed, fmt.Sprintf("  %s: %v", r.ID, r.Err))
		} else {
			ok = append(ok, r.ID)
		}
	}
	sort.Strings(ok)
	fmt.Fprintf(out, "\n%s — %d ok, %d failed\n", action, len(ok), len(failed))
	if len(ok) > 0 {
		fmt.Fprintf(out, "  ok: %s\n", strings.Join(ok, ", "))
	}
	if len(failed) > 0 {
		fmt.Fprintln(out, "  failed:")
		sort.Strings(failed)
		for _, l := range failed {
			fmt.Fprintln(out, l)
		}
		return fmt.Errorf("%d of %d sandbox(es) failed", len(failed), len(results))
	}
	return nil
}

// statusFleetTable dials every target concurrently and renders the fleet overview table
// (§14.3/14.6): one row per member — reachability, heartbeat, queue, runtimes. An unreachable
// member renders a row (it never aborts the others).
func statusFleetTable(cmd *cobra.Command, targets []*config.Config) error {
	type row struct{ id, link, hb, queue, runtimes string }
	rows := make([]row, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, fanOutConcurrency)
	for i, c := range targets {
		i, c := i, c
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(cmd.Context(), 40*time.Second)
			defer cancel()
			sess, err := openSession(ctx, c, transport)
			if err != nil {
				rows[i] = row{c.SandboxID, "✗ unreachable", "—", "—", "—"}
				return
			}
			defer sess.Close()
			s := collectStatus(ctx, sess.fs, sess.runner, sess.tree)
			hb := "—"
			if s.HeartbeatSeq != "" {
				hb = "seq " + s.HeartbeatSeq
				if s.HeartbeatAge != "" {
					hb += " (~" + s.HeartbeatAge + ")"
				}
			}
			rt := "none"
			if len(s.Runtimes) > 0 {
				rt = strings.Join(s.Runtimes, ",")
			}
			rows[i] = row{c.SandboxID, "● reachable", hb, fmt.Sprintf("%d/%d", s.QueueOut, s.QueueIn), rt}
		}()
	}
	wg.Wait()
	w := cmd.OutOrStdout()
	const f = "%-16s %-14s %-24s %-8s %s\n"
	fmt.Fprintf(w, f, "SANDBOX", "LINK", "HEARTBEAT", "QUEUE", "RUNTIMES")
	for _, r := range rows {
		fmt.Fprintf(w, f, r.id, r.link, r.hb, r.queue, r.runtimes)
	}
	return nil
}

// prefixWriter prefixes every complete line written through it with prefix, writing to the
// shared w under mu. Partial lines buffer until their newline arrives; flush() emits any tail.
type prefixWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix string
	buf    []byte
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf = append(p.buf, b...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			break
		}
		_, _ = io.WriteString(p.w, p.prefix)
		_, _ = p.w.Write(p.buf[:i+1])
		p.buf = p.buf[i+1:]
	}
	return len(b), nil
}

// flush emits any buffered partial line (adding a newline) so nothing is lost at op end.
func (p *prefixWriter) flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.buf) > 0 {
		_, _ = io.WriteString(p.w, p.prefix)
		_, _ = p.w.Write(p.buf)
		_, _ = io.WriteString(p.w, "\n")
		p.buf = nil
	}
}
