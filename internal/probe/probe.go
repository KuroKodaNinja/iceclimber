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
	OS              string        `json:"os"`     // normalized: "linux", "darwin", ...
	OSRaw           string        `json:"os_raw"` // uname -s verbatim
	Arch            string        `json:"arch"`   // uname -m verbatim, e.g. "x86_64", "aarch64"
	Home            string        `json:"home"`
	Libc            Libc          `json:"libc"` // meaningful on Linux only
	Roots           []RootInfo    `json:"roots"`
	Runtimes        []RuntimeInfo `json:"runtimes,omitempty"` // language runtimes found on PATH (brownfield)
	HasExistingTree bool          `json:"has_existing_tree"`
	Warnings        []string      `json:"warnings,omitempty"`
}

// RuntimeInfo describes a language runtime discovered on the sandbox's PATH — the
// raw material for "use a pre-existing runtime" (brownfield) mode. It is reported by
// probe but never acted on here; the operator chooses a source at bootstrap.
type RuntimeInfo struct {
	Lang        string   `json:"lang"`                   // "python" | "node" | "java"
	Path        string   `json:"path"`                   // resolved binary (command -v)
	Version     string   `json:"version,omitempty"`      // parsed, e.g. "3.11.2"
	VersionRaw  string   `json:"version_raw,omitempty"`  // the unparsed --version line
	EnvManagers []string `json:"env_managers,omitempty"` // python only: "venv","conda"
}

// Runtime returns the discovered runtime for lang ("python"/"node"/"java"), if present.
func (f *Fingerprint) Runtime(lang string) (RuntimeInfo, bool) {
	for _, rt := range f.Runtimes {
		if rt.Lang == lang && rt.Path != "" {
			return rt, true
		}
	}
	return RuntimeInfo{}, false
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
echo "GLIBC=$(getconf GNU_LIBC_VERSION 2>/dev/null)"
if command -v python3 >/dev/null 2>&1; then
  echo "PY_PATH=$(command -v python3)"
  echo "PY_VER=$(python3 --version 2>&1 | head -n1)"
  python3 -c 'import venv' 2>/dev/null && echo "PY_VENV=yes"
  python3 -c 'import ensurepip' 2>/dev/null && echo "PY_ENSUREPIP=yes"
fi
command -v conda >/dev/null 2>&1 && echo "CONDA_PATH=$(command -v conda)"
if command -v node >/dev/null 2>&1; then
  echo "NODE_PATH=$(command -v node)"
  echo "NODE_VER=$(node --version 2>&1 | head -n1)"
fi
if command -v java >/dev/null 2>&1; then
  echo "JAVA_PATH=$(command -v java)"
  echo "JAVA_VER=$(java -version 2>&1 | head -n1)"
fi`

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
	fp.Runtimes = detectRuntimes(kv)

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

// detectRuntimes turns the probe script's runtime keys into RuntimeInfo entries —
// only for runtimes actually found on PATH (a missing runtime emits no keys). Python
// also reports its usable env managers: "venv" (only when both the venv module AND
// ensurepip import — Debian ships python3 without a working venv otherwise) and
// "conda" when a conda binary is on PATH.
func detectRuntimes(kv map[string]string) []RuntimeInfo {
	var out []RuntimeInfo
	if p := kv["PY_PATH"]; p != "" {
		rt := RuntimeInfo{Lang: "python", Path: p, VersionRaw: kv["PY_VER"], Version: parsePythonVersion(kv["PY_VER"])}
		if kv["PY_VENV"] == "yes" && kv["PY_ENSUREPIP"] == "yes" {
			rt.EnvManagers = append(rt.EnvManagers, "venv")
		}
		if kv["CONDA_PATH"] != "" {
			rt.EnvManagers = append(rt.EnvManagers, "conda")
		}
		out = append(out, rt)
	}
	if p := kv["NODE_PATH"]; p != "" {
		out = append(out, RuntimeInfo{Lang: "node", Path: p, VersionRaw: kv["NODE_VER"], Version: parseNodeVersion(kv["NODE_VER"])})
	}
	if p := kv["JAVA_PATH"]; p != "" {
		out = append(out, RuntimeInfo{Lang: "java", Path: p, VersionRaw: kv["JAVA_VER"], Version: parseJavaVersion(kv["JAVA_VER"])})
	}
	return out
}

// parsePythonVersion: "Python 3.11.2" -> "3.11.2".
func parsePythonVersion(s string) string {
	if f := strings.Fields(s); len(f) >= 2 {
		return f[1]
	}
	return ""
}

// parseNodeVersion: "v20.1.0" -> "20.1.0".
func parseNodeVersion(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "v")
}

// parseJavaVersion: `openjdk version "17.0.1" 2021-…` or `java version "1.8.0_292"`
// -> the first quoted token.
func parseJavaVersion(s string) string {
	if i := strings.IndexByte(s, '"'); i >= 0 {
		if j := strings.IndexByte(s[i+1:], '"'); j >= 0 {
			return s[i+1 : i+1+j]
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
