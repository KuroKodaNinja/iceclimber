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
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/relay"
	"github.com/KuroKodaNinja/iceclimber/internal/runtime"
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

// fleetTargetsMutate resolves members for a MUTATING fan-out (install*/bootstrap/agent install).
// A missing --sandbox is fine for a SINGLE-box setup (unambiguous), but an ERROR when the fleet
// has >1 member — a forgotten flag must never fan a mutation to N boxes (§14.2).
func fleetTargetsMutate() ([]*config.Config, error) {
	f, err := resolveFleet()
	if err != nil {
		return nil, err
	}
	if sandboxID == "" {
		if len(f.Members()) == 1 {
			return f.Members(), nil
		}
		return nil, fmt.Errorf("--sandbox is required to fan this out across the fleet (%s) — name a box, a glob, a list, or `--sandbox all`",
			strings.Join(f.IDs(), ", "))
	}
	return f.Resolve(sandboxID)
}

// runFleetMutation runs a mutating command single-box (as today: direct output, fail-fast) or
// fanned out across the --sandbox targets ([box]-prefixed, keep-going, summary + exit 1). op
// gets an already-open, ops-locked session for one member and its output writer. §14.2/14.5.
func runFleetMutation(cmd *cobra.Command, timeout time.Duration, mode, action string, op func(ctx context.Context, cfg *config.Config, sess *session, out io.Writer) error) error {
	targets, err := fleetTargetsMutate()
	if err != nil {
		return err
	}
	one := func(ctx context.Context, cfg *config.Config, out io.Writer) error {
		ictx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		sess, err := openLockedSession(ictx, cfg, transport, mode)
		if err != nil {
			return err
		}
		defer sess.Close()
		return op(ictx, cfg, sess, out)
	}
	if len(targets) == 1 {
		return one(cmd.Context(), targets[0], cmd.OutOrStdout())
	}
	results := fanOut(cmd.Context(), cmd.OutOrStdout(), targets, one)
	return summarizeFanOut(cmd.OutOrStdout(), action, results)
}

// parallel runs fn(i) for i in [0,n) with bounded concurrency (fanOutConcurrency), waiting
// for all — the shared shape of the fan-out and two-phase-install loops.
func parallel(n int, fn func(i int)) {
	sem := make(chan struct{}, fanOutConcurrency)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}()
	}
	wg.Wait()
}

// progressNop discards install progress events (used during the fleet resolve phase, where a
// live per-member byte meter would be noise).
func progressNop(progress.Event) {}

// printInstallResult renders a runtime install result the way the single-box commands do.
func printInstallResult(out io.Writer, lang string, res runtime.Result) {
	verb := "installed"
	if res.AlreadyInstalled {
		verb = "already installed:"
	}
	fmt.Fprintf(out, "%s %s %s at %s\n", verb, lang, res.Version, res.Path)
}

// runFleetInstall installs a runtime single-box (today's path: live progress, fail-fast) or
// across the fleet with RESOLVE-VERSION-ONCE (§14.5): phase 1 opens a session + resolves each
// member's platform asset; the resolved full versions are asserted equal (N boxes must not land
// on different patch releases if the upstream index moves mid-fan-out); phase 2 installs the
// resolved pin on each member concurrently ([box]-prefixed). The cache's per-asset flock makes
// the same asset download once and push N.
func runFleetInstall(cmd *cobra.Command, timeout time.Duration, lang, version string, mk func(*session, progress.Func) *runtime.Installer, post func(context.Context, *session, runtime.Result) error) error {
	targets, err := fleetTargetsMutate()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	if len(targets) == 1 {
		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		defer cancel()
		sess, err := openLockedSession(ctx, targets[0], transport, "install")
		if err != nil {
			return err
		}
		defer sess.Close()
		pr, finish := installProgress(out, sess.transport)
		res, err := mk(sess, pr).Install(ctx, version)
		finish()
		if err != nil {
			return err
		}
		if post != nil {
			if err := post(ctx, sess, res); err != nil {
				return err
			}
		}
		printInstallResult(out, lang, res)
		return nil
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()

	type member struct {
		cfg  *config.Config
		sess *session
		inst *runtime.Installer
		pin  relay.Pin
		err  error
	}
	ms := make([]*member, len(targets))
	parallel(len(targets), func(i int) {
		m := &member{cfg: targets[i]}
		ms[i] = m
		sess, err := openLockedSession(ctx, m.cfg, transport, "install")
		if err != nil {
			m.err = err
			return
		}
		m.sess = sess
		m.inst = mk(sess, progressNop)
		m.pin, m.err = m.inst.Resolve(ctx, version)
	})
	defer func() {
		for _, m := range ms {
			if m != nil && m.sess != nil {
				_ = m.sess.Close()
			}
		}
	}()

	// Version consistency across everyone who resolved (§14.5).
	resolved := ""
	for _, m := range ms {
		if m.err == nil {
			if resolved == "" {
				resolved = m.pin.FullVersion
			} else if m.pin.FullVersion != resolved {
				return fmt.Errorf("fleet resolved %s to different versions (%s vs %s) — the upstream index moved mid-resolve; retry", lang, resolved, m.pin.FullVersion)
			}
		}
	}
	if resolved != "" {
		fmt.Fprintf(out, "resolved %s → %s; installing on %d sandboxes\n", lang, resolved, len(targets))
	}

	var mu sync.Mutex
	results := make([]fanResult, len(targets))
	parallel(len(targets), func(i int) {
		m := ms[i]
		pw := &prefixWriter{mu: &mu, w: out, prefix: "[" + m.cfg.SandboxID + "] "}
		defer pw.flush()
		if m.err != nil {
			results[i] = fanResult{ID: m.cfg.SandboxID, Err: m.err}
			return
		}
		res, ierr := m.inst.InstallPin(ctx, m.pin)
		if ierr == nil && post != nil {
			ierr = post(ctx, m.sess, res)
		}
		if ierr == nil {
			printInstallResult(pw, lang, res)
		}
		results[i] = fanResult{ID: m.cfg.SandboxID, Err: ierr}
	})
	return summarizeFanOut(out, "install "+lang, results)
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
