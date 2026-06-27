// Package probe fingerprints the sandbox host over a remote.Runner: OS, arch,
// libc, and per-candidate install-root viability (real write tests, not just
// permission bits). It performs no installs and writes nothing durable. Every
// command it issues is plain POSIX sh — no bashisms, no GNU-only flags (§7).
package probe

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/remote"
)

// Options controls a probe run.
type Options struct {
	// ExplicitRoots are operator-supplied candidate install roots, tested first
	// and in order, ahead of the built-in defaults ($HOME/.iceclimber, then
	// /opt/iceclimber).
	ExplicitRoots []string
	// RemoteRoot is where an existing iceclimber tree would live. When empty,
	// $HOME/.iceclimber is checked.
	RemoteRoot string
}

// Fingerprint is the result of probing a sandbox host.
type Fingerprint struct {
	OS              string     `json:"os"`     // normalized: "linux", "darwin", ...
	OSRaw           string     `json:"os_raw"` // uname -s verbatim
	Arch            string     `json:"arch"`   // uname -m verbatim, e.g. "x86_64", "aarch64"
	Home            string     `json:"home"`
	Libc            Libc       `json:"libc"` // meaningful on Linux only
	Roots           []RootInfo `json:"roots"`
	HasExistingTree bool       `json:"has_existing_tree"`
	Warnings        []string   `json:"warnings,omitempty"`
}

// Libc describes the C library family detected on a Linux host.
type Libc struct {
	Family         string   `json:"family"` // "glibc", "musl", "unknown", "n/a"
	Version        string   `json:"version,omitempty"`
	HighConfidence bool     `json:"high_confidence"`
	Signals        []string `json:"signals,omitempty"` // raw evidence, for logs/operator
}

// RootInfo is the viability of one candidate install root.
type RootInfo struct {
	Path      string `json:"path"`
	Creatable bool   `json:"creatable"` // mkdir -p succeeded
	Writable  bool   `json:"writable"`  // wrote a file and read the same bytes back
	AvailKB   int64  `json:"avail_kb"`  // available space on the containing filesystem
}

// FirstViableRoot returns the first creatable+writable candidate, or "".
func (f *Fingerprint) FirstViableRoot() string {
	for _, r := range f.Roots {
		if r.Creatable && r.Writable {
			return r.Path
		}
	}
	return ""
}

const systemScript = `echo "OS=$(uname -s 2>/dev/null)"
echo "ARCH=$(uname -m 2>/dev/null)"
echo "HOME=$HOME"
echo "LDD=$(ldd --version 2>&1 | head -n1)"
echo "MUSL=$(ls -d /lib/ld-musl-* 2>/dev/null | head -n1)"
echo "GLIBC=$(getconf GNU_LIBC_VERSION 2>/dev/null)"`

// Run fingerprints the host.
func Run(ctx context.Context, r remote.Runner, opts Options) (*Fingerprint, error) {
	res, err := r.Run(ctx, systemScript, nil)
	if err != nil {
		return nil, fmt.Errorf("probe system info: %w", err)
	}
	fp := &Fingerprint{}
	kv := parseKV(string(res.Stdout))
	fp.OSRaw = kv["OS"]
	fp.OS = strings.ToLower(kv["OS"])
	fp.Arch = kv["ARCH"]
	fp.Home = kv["HOME"]
	fp.Libc = detectLibc(fp.OS, kv)

	for _, root := range candidateRoots(opts.ExplicitRoots, fp.Home) {
		info, err := probeRoot(ctx, r, root)
		if err != nil {
			return nil, fmt.Errorf("probe root %s: %w", root, err)
		}
		fp.Roots = append(fp.Roots, info)
	}

	treeRoot := opts.RemoteRoot
	if treeRoot == "" && fp.Home != "" {
		treeRoot = fp.Home + "/.iceclimber"
	}
	if treeRoot != "" {
		exists, err := detectExistingTree(ctx, r, treeRoot)
		if err != nil {
			return nil, fmt.Errorf("detect existing tree: %w", err)
		}
		fp.HasExistingTree = exists
	}

	finalizeWarnings(fp)
	return fp, nil
}

// candidateRoots applies the §7 step-3 ordering: operator-supplied paths first,
// then $HOME/.iceclimber, then /opt/iceclimber as a root-only long shot.
func candidateRoots(explicit []string, home string) []string {
	roots := append([]string{}, explicit...)
	if home != "" {
		roots = append(roots, home+"/.iceclimber")
	}
	roots = append(roots, "/opt/iceclimber")
	return roots
}

func detectLibc(goos string, kv map[string]string) Libc {
	if goos != "linux" {
		return Libc{Family: "n/a", HighConfidence: true}
	}
	var lc Libc
	ldd := kv["LDD"]
	musl := kv["MUSL"]
	glibc := kv["GLIBC"]

	muslSig := musl != "" || strings.Contains(strings.ToLower(ldd), "musl")
	glibcSig := glibc != "" || containsAny(strings.ToLower(ldd), "gnu libc", "glibc", "gnu c library")

	if musl != "" {
		lc.Signals = append(lc.Signals, "ld-musl present: "+musl)
	}
	if ldd != "" {
		lc.Signals = append(lc.Signals, "ldd: "+ldd)
	}
	if glibc != "" {
		lc.Signals = append(lc.Signals, "getconf GNU_LIBC_VERSION: "+glibc)
	}

	switch {
	case muslSig && !glibcSig:
		lc.Family = "musl"
		lc.HighConfidence = true
	case glibcSig && !muslSig:
		lc.Family = "glibc"
		lc.Version = parseGlibcVersion(glibc, ldd)
		lc.HighConfidence = true
	default:
		// No signal, or conflicting signals — neither is trustworthy.
		lc.Family = "unknown"
		lc.HighConfidence = false
	}
	return lc
}

func parseGlibcVersion(glibc, ldd string) string {
	if f := strings.Fields(glibc); len(f) >= 2 {
		return f[len(f)-1] // "glibc 2.31" -> "2.31"
	}
	if f := strings.Fields(ldd); len(f) > 0 {
		last := f[len(f)-1] // "ldd (GNU libc) 2.31" -> "2.31"
		if strings.ContainsAny(last, "0123456789") {
			return last
		}
	}
	return ""
}

func probeRoot(ctx context.Context, r remote.Runner, root string) (RootInfo, error) {
	info := RootInfo{Path: root}
	q := remote.ShellQuote(root)
	script := `r=` + q + `
mkdir -p "$r" 2>/dev/null || { echo "MKDIR=fail"; exit 0; }
echo "MKDIR=ok"
echo "DF=$(df -Pk "$r" 2>/dev/null | tail -n1)"
t="$r/.iceclimber-writetest"
if printf %s ok > "$t" 2>/dev/null && [ "$(cat "$t" 2>/dev/null)" = ok ]; then echo "WRITE=ok"; else echo "WRITE=fail"; fi
rm -f "$t" 2>/dev/null`
	res, err := r.Run(ctx, script, nil)
	if err != nil {
		return info, err
	}
	kv := parseKV(string(res.Stdout))
	info.Creatable = kv["MKDIR"] == "ok"
	info.Writable = kv["WRITE"] == "ok"
	info.AvailKB = parseDFAvail(kv["DF"])
	return info, nil
}

func detectExistingTree(ctx context.Context, r remote.Runner, root string) (bool, error) {
	q := remote.ShellQuote(root)
	script := `if [ -e ` + q + `/protocol ] || [ -e ` + q + `/state/manifest.json ]; then echo "EXISTS=yes"; else echo "EXISTS=no"; fi`
	res, err := r.Run(ctx, script, nil)
	if err != nil {
		return false, err
	}
	return parseKV(string(res.Stdout))["EXISTS"] == "yes", nil
}

func finalizeWarnings(fp *Fingerprint) {
	if fp.OS == "" {
		fp.Warnings = append(fp.Warnings, "could not determine OS (uname -s returned nothing)")
	}
	if fp.Arch == "" {
		fp.Warnings = append(fp.Warnings, "could not determine architecture (uname -m returned nothing)")
	}
	if fp.OS == "linux" && !fp.Libc.HighConfidence {
		fp.Warnings = append(fp.Warnings, "libc family is low-confidence ("+fp.Libc.Family+"); confirm before installing Python")
	}
	if fp.FirstViableRoot() == "" {
		fp.Warnings = append(fp.Warnings, "no candidate install root is writable; bootstrap will require an explicit --root")
	}
}

// parseDFAvail extracts the Available column (field 4) from a `df -Pk` data
// line. POSIX -P guarantees one logical line per filesystem, no wrapping.
func parseDFAvail(dfLine string) int64 {
	f := strings.Fields(dfLine)
	if len(f) < 4 {
		return 0
	}
	v, err := strconv.ParseInt(f[3], 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// parseKV reads lines of the form KEY=value (split on the first '=').
func parseKV(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		i := strings.IndexByte(line, '=')
		if i < 0 {
			continue
		}
		m[line[:i]] = strings.TrimSpace(line[i+1:])
	}
	return m
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
