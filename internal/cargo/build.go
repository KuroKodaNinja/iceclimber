// Package cargo runs the Rust Cargo build tool on a sandbox project — the Rust analogue of
// internal/maven's maven.build. Two tiers, mirroring maven.build:
//
//   - proxy (egress_mode: proxy): the sandbox's own cargo builds ONLINE, fetching crates.io
//     through Popo's MITM egress proxy (cargo honors HTTPS_PROXY + CARGO_HTTP_CAINFO). No
//     controller-side cargo, no vendoring: the proxy IS the network mediation.
//   - relay (else): air-gapped. The controller's cargo `cargo vendor`s every crate the
//     project's Cargo.lock pins, Popo relays that vendor tree into the sandbox, a
//     .cargo/config.toml points cargo at it, and the sandbox builds OFFLINE
//     (`cargo build --release --offline`). Nothing leaves the sandbox's disk.
package cargo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/remote"
	"github.com/KuroKodaNinja/iceclimber/internal/remotefs"
	"github.com/KuroKodaNinja/iceclimber/internal/rust"
)

// Deps are what a cargo.build needs: where to run (sandbox FS + Runner), the platform (to
// locate the sandbox cargo), whether egress is proxied, and the controller's cargo (for the
// relay/vendor tier).
type Deps struct {
	FS              remotefs.FS
	Runner          remote.Runner
	Root            string
	Arch            string
	Libc            string
	EgressProxy     bool   // egress_mode: proxy → build ONLINE through the MITM proxy (no vendor relay)
	ControllerCargo string // operator's cargo on the controller (default "cargo"), for the relay tier
	CacheDir        string // controller CARGO_HOME scoping (relay tier); empty = cargo's default
	Progress        progress.Func
}

// BuildResult is the cargo.build response body: the compiled executable paths (sandbox paths)
// and the tier used.
type BuildResult struct {
	Artifacts []string `json:"artifacts"`
	Tier      string   `json:"tier"`
}

// Build runs a release `cargo build` of a sandbox project. projectDir is a sandbox directory
// holding a Cargo.toml. rustVersion selects the already-installed sandbox toolchain (its cargo);
// extraArgs are passed through to cargo after the fixed flags.
func Build(ctx context.Context, d Deps, projectDir, rustVersion string, extraArgs []string) (BuildResult, error) {
	cargoBin, err := rust.Locate(ctx, d.FS, d.Root, rustVersion, d.Arch, d.Libc)
	if err != nil {
		return BuildResult{}, err
	}
	if d.EgressProxy {
		return d.buildOnlineProxy(ctx, cargoBin, projectDir, extraArgs)
	}
	return d.buildRelay(ctx, cargoBin, projectDir, extraArgs)
}

// buildOnlineProxy builds in egress-proxy mode: the sandbox's cargo fetches crates.io through
// Popo's MITM proxy. cargo honors HTTPS_PROXY + CARGO_HTTP_CAINFO natively (no truststore
// dance like the JVM needs), so trust + routing come straight from the bootstrap-written
// egress-env.sh plus an explicit CARGO_HTTP_CAINFO pointing at the egress CA.
func (d Deps) buildOnlineProxy(ctx context.Context, cargoBin, projectDir string, extraArgs []string) (BuildResult, error) {
	envScript := path.Join(d.Root, "egress-env.sh")
	if !d.exists(ctx, envScript) {
		return BuildResult{}, fmt.Errorf("proxy-mode `cargo build` needs the egress trust bootstrap (egress-env.sh) — run `iceclimber bootstrap` in egress_mode: proxy, or use egress_mode: relay for an air-gapped build")
	}
	caPath := path.Join(d.Root, "certs", "egress-ca.pem")

	d.Progress.Phase("building (online via proxy)")
	// Source egress-env.sh (HTTPS_PROXY + the CA env vars, port baked in at bootstrap), then
	// pin CARGO_HTTP_CAINFO explicitly so cargo validates the proxy's minted leaves.
	prefix := "export ICECLIMBER_HOME=" + remote.ShellQuote(d.Root) + " && . " + remote.ShellQuote(envScript) +
		" && export CARGO_HTTP_CAINFO=" + remote.ShellQuote(caPath) + " && cd " + remote.ShellQuote(projectDir) + " && "
	cmd := prefix + cargoBuildCmd(cargoBin, extraArgs, false)

	res, err := d.Runner.Run(ctx, cmd, nil)
	if err != nil {
		return BuildResult{}, fmt.Errorf("run online cargo: %w", err)
	}
	if res.ExitCode != 0 {
		combined := string(res.Stdout) + string(res.Stderr)
		if looksLikeProxyDown(combined) {
			return BuildResult{}, fmt.Errorf("online cargo build failed — the egress proxy is not reachable: proxy-mode `cargo build` needs an active `iceclimber serve` (the proxy only listens during a serve session), or use egress_mode: relay for a one-shot build:\n%s", pkg.LastLines([]byte(combined), 8))
		}
		return BuildResult{}, fmt.Errorf("online cargo build failed: %s", pkg.LastLines([]byte(combined), 8))
	}
	return BuildResult{Artifacts: parseArtifacts(res.Stdout), Tier: "proxy"}, nil
}

// buildRelay builds air-gapped: the controller's cargo vendors every crate the project's
// Cargo.lock pins, Popo relays that vendor tree into the sandbox, a .cargo/config.toml points
// cargo at it, and the sandbox builds offline. The controller only downloads + writes files
// from the vendored crates — it never runs the project's build script (that happens offline in
// the sandbox), so an untrusted Cargo.toml cannot execute code on the controller.
func (d Deps) buildRelay(ctx context.Context, cargoBin, projectDir string, extraArgs []string) (BuildResult, error) {
	ccargo := pkg.FirstNonEmpty(d.ControllerCargo, "cargo")
	if out, err := exec.CommandContext(ctx, ccargo, "--version").CombinedOutput(); err != nil {
		return BuildResult{}, fmt.Errorf("cargo.build relay mode needs cargo on the controller (set controller_cargo): %v: %s", err, pkg.LastLines(out, 3))
	}

	// 1. Read the project's Cargo.toml (and Cargo.lock if present) from the SANDBOX into a
	//    controller temp dir. Reading known paths avoids walking sandbox-controlled entries.
	d.Progress.Phase("staging manifest")
	stage, err := os.MkdirTemp("", "iceclimber-cargobuild-")
	if err != nil {
		return BuildResult{}, err
	}
	defer os.RemoveAll(stage)
	tomlBytes, err := d.FS.ReadFile(ctx, path.Join(projectDir, "Cargo.toml"))
	if err != nil {
		return BuildResult{}, fmt.Errorf("read %s/Cargo.toml (create the Cargo project first): %w", projectDir, err)
	}
	stageToml := filepath.Join(stage, "Cargo.toml")
	if err := os.WriteFile(stageToml, tomlBytes, 0o644); err != nil {
		return BuildResult{}, err
	}
	haveLock := false
	if lockBytes, lerr := d.FS.ReadFile(ctx, path.Join(projectDir, "Cargo.lock")); lerr == nil {
		if err := os.WriteFile(filepath.Join(stage, "Cargo.lock"), lockBytes, 0o644); err != nil {
			return BuildResult{}, err
		}
		haveLock = true
	}

	env := d.controllerEnv()
	// 2. Without a Cargo.lock, pin one on the controller so `--locked` vendoring is deterministic.
	if !haveLock {
		if out, err := d.runController(ctx, ccargo, env, "generate-lockfile", "--manifest-path", stageToml); err != nil {
			return BuildResult{}, fmt.Errorf("controller `cargo generate-lockfile` failed: %s", pkg.LastLines(out, 8))
		}
	}

	// 3. Vendor every locked crate on the controller (downloads them). cargo prints the
	//    .cargo/config.toml source-replacement snippet to stdout.
	d.Progress.Phase("vendoring (controller)")
	vendorDir := filepath.Join(stage, "vendor")
	snippet, err := d.runController(ctx, ccargo, env, "vendor", "--manifest-path", stageToml, "--locked", vendorDir)
	if err != nil {
		return BuildResult{}, fmt.Errorf("controller `cargo vendor` failed: %v", err)
	}

	// 4. Relay the vendor tree into the sandbox project and write the .cargo/config.toml that
	//    points cargo at it (directory rewritten to the sandbox vendor path).
	d.Progress.Phase("relaying vendor")
	sandboxVendor := path.Join(projectDir, "vendor")
	if err := pushTree(ctx, d.FS, d.Progress, vendorDir, sandboxVendor); err != nil {
		return BuildResult{}, fmt.Errorf("relay vendor tree: %w", err)
	}
	config := rewriteVendorConfig(string(snippet), sandboxVendor)
	if err := d.FS.Mkdir(ctx, path.Join(projectDir, ".cargo")); err != nil {
		return BuildResult{}, err
	}
	if err := d.FS.WriteFile(ctx, path.Join(projectDir, ".cargo", "config.toml"), []byte(config)); err != nil {
		return BuildResult{}, fmt.Errorf("write .cargo/config.toml: %w", err)
	}

	// 5. Sandbox builds OFFLINE from the relayed vendor dir.
	d.Progress.Phase("building (offline)")
	cmd := "cd " + remote.ShellQuote(projectDir) + " && " + cargoBuildCmd(cargoBin, extraArgs, true)
	res, err := d.Runner.Run(ctx, cmd, nil)
	if err != nil {
		return BuildResult{}, fmt.Errorf("run offline cargo: %w", err)
	}
	if res.ExitCode != 0 {
		combined := string(res.Stdout) + string(res.Stderr)
		return BuildResult{}, fmt.Errorf("offline cargo build failed: %s", pkg.LastLines([]byte(combined), 8))
	}
	return BuildResult{Artifacts: parseArtifacts(res.Stdout), Tier: "relay"}, nil
}

// controllerEnv scopes the controller cargo's downloads under CacheDir (its own CARGO_HOME),
// falling back to cargo's default when no cache dir is configured.
func (d Deps) controllerEnv() []string {
	env := os.Environ()
	if d.CacheDir != "" {
		env = append(env, "CARGO_HOME="+filepath.Join(d.CacheDir, "cargo-home"))
	}
	return env
}

// runController runs a controller-side cargo command, returning its combined output.
// runController runs the controller cargo capturing stdout and stderr SEPARATELY, returning
// stdout. `cargo vendor` prints the .cargo/config.toml source-replacement snippet to stdout but
// its progress ("Downloading crates …", "Vendoring …") to stderr — combining them corrupts the
// TOML we relay, so they must stay apart. On failure the error carries stderr for diagnostics.
func (d Deps) runController(ctx context.Context, cargoBin string, env []string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, cargoBin, args...)
	c.Env = env
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("%w: %s", err, pkg.LastLines(stderr.Bytes(), 8))
	}
	return stdout.Bytes(), nil
}

// exists reports whether a sandbox path is present (a file listed in its parent dir).
func (d Deps) exists(ctx context.Context, p string) bool {
	names, err := d.FS.List(ctx, path.Dir(p))
	if err != nil {
		return false
	}
	base := path.Base(p)
	for _, n := range names {
		if n == base {
			return true
		}
	}
	return false
}

// cargoBuildCmd renders the `cargo build --release --message-format=json [--offline] [args…]`
// invocation (shell-quoted). --message-format=json makes cargo emit one JSON object per line so
// the compiled executable paths can be parsed from stdout.
func cargoBuildCmd(cargoBin string, extraArgs []string, offline bool) string {
	// cargo, run by absolute path without rustup, finds rustc via $RUSTC / $PATH — not adjacent
	// to itself. Point it at the sibling rustc in the same bin/ and put that bin/ on PATH so the
	// compiler + its helper tools (rust-lld, etc.) resolve during the build.
	binDir := path.Dir(cargoBin)
	env := "RUSTC=" + remote.ShellQuote(path.Join(binDir, "rustc")) +
		" PATH=" + remote.ShellQuote(binDir) + `:"$PATH" `
	parts := []string{remote.ShellQuote(cargoBin), "build", "--release", "--message-format=json"}
	if offline {
		parts = append(parts, "--offline")
	}
	for _, a := range extraArgs {
		parts = append(parts, remote.ShellQuote(a))
	}
	return env + strings.Join(parts, " ")
}

// cargoMessage is one line of `cargo build --message-format=json`. A compiler-artifact whose
// `executable` is non-null is a built binary.
type cargoMessage struct {
	Reason     string  `json:"reason"`
	Executable *string `json:"executable"`
}

// parseArtifacts collects the compiled executable paths from cargo's JSON-lines stdout. Lines
// that aren't cargo messages (or aren't compiler-artifacts with an executable) are skipped.
func parseArtifacts(stdout []byte) []string {
	var arts []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var m cargoMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if m.Reason == "compiler-artifact" && m.Executable != nil && *m.Executable != "" && !seen[*m.Executable] {
			seen[*m.Executable] = true
			arts = append(arts, *m.Executable)
		}
	}
	sort.Strings(arts)
	return arts
}

// rewriteVendorConfig produces the sandbox's .cargo/config.toml from cargo vendor's printed
// source-replacement snippet, rewriting the `directory = …` line (the controller-side vendor
// path) to the sandbox vendor path. If the snippet is empty/shapeless, a canonical crates-io
// replacement config is synthesized instead.
func rewriteVendorConfig(snippet, sandboxVendorDir string) string {
	snippet = strings.TrimSpace(snippet)
	if snippet == "" {
		return defaultVendorConfig(sandboxVendorDir)
	}
	lines := strings.Split(snippet, "\n")
	rewrote := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "directory") && strings.Contains(l, "=") {
			indent := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
			lines[i] = indent + `directory = "` + sandboxVendorDir + `"`
			rewrote = true
		}
	}
	if !rewrote {
		return defaultVendorConfig(sandboxVendorDir)
	}
	return strings.Join(lines, "\n") + "\n"
}

func defaultVendorConfig(sandboxVendorDir string) string {
	return "[source.crates-io]\n" +
		"replace-with = \"vendored-sources\"\n\n" +
		"[source.vendored-sources]\n" +
		"directory = \"" + sandboxVendorDir + "\"\n"
}

// looksLikeProxyDown reports whether cargo's output indicates it couldn't reach the egress
// proxy on the sandbox loopback — the tell that proxy-mode `cargo build` was run without an
// active serve holding the reverse tunnel up.
func looksLikeProxyDown(out string) bool {
	for _, m := range []string{"Connection refused", "connection refused", "error trying to connect", "127.0.0.1:", "failed to connect"} {
		if strings.Contains(out, m) {
			return true
		}
	}
	return false
}
