# iceclimber — agent guide

`iceclimber` is a Go CLI for operating a Claude agent running in YOLO mode inside
a sandbox it can't otherwise provision (Python, packages, web access), over an
**SSH-only** link. One binary, two roles: **Popo** (the controller, your laptop)
and **Nana** (the sandbox-side persona).

**Design source of truth:** [`ice-climbers-plan.md`](./ice-climbers-plan.md).
Read it before any substantial work — especially **§0 (v1/v2 scope)**, **§11
(decision log)**, and **§12 (build phases + testing strategy)**. It is a *living*
doc: keep it current as decisions settle.

## Global guidance (washu kit)

This project follows the user's global coding kit, canonical location
`~/Coding/ai/washu` (symlinked here as `.agents` for convenience; gitignored, so
it may be absent — use the canonical path if so). Read `languages/general` +
`languages/go` and `quality/{testing,security}`. The points that bite here:
simplicity-first (no speculative machinery), errors-as-values, accept-interfaces/
return-structs, table-driven tests run with `-race`, and secure-by-default (e.g.
no `InsecureIgnoreHostKey`).

## Working agreement

- **Tight v1, parked v2.** Ship the minimum that works; park sophisticated ideas
  as v2 with a named re-entry trigger (plan §0). Push back on speculative scope
  rather than build it.
- **Test as we build.** Every phase gets a real functional/E2E test, not just
  unit tests. The harness drives the *real binary* against a Lima/Alpine sandbox
  (`test/functional/`, `//go:build functional`). See [`test/README.md`](./test/README.md).
- **TUI flows are tested as a rule.** Every console flow — every key, modal, form
  field, and state transition — has a flow test driving the *real* Bubble Tea
  runtime via `teatest` (`internal/tui/flows_test.go`), the interactive analogue of
  the CLI's stdin/stdout tests. New TUI behaviour ⇒ a new flow test. The console's
  executor also has a live-VM functional test (`make tui-functional`).
- **Keep the acceptance demo current.** The demo proves the *premise* — a real
  Claude agent, air-gapped, building real software bridged through Popo. **Whenever a
  major feature or a language is added, refresh it** (`test/demo/TASK.md` +
  `test/lima/demo-verify.sh`) so the premise stays proven for what we've actually
  built, then re-run `make demo`. See [`DEMO.md`](./DEMO.md).
- **Commits.** Conventional Commits, atomic and self-contained — each commit
  builds and passes tests on its own. See `.agents/git/commits`.

## Adding a language

A language is only "done" when it has **the same treatment as Python, JavaScript,
and Java**. Use this checklist (Python/Node/Java are the worked examples; decisions
#22, #44, #51, #52). Stage it: **runtime first, then the package manager.**

1. **De-risk distribution first.** Before writing code, confirm a portable runtime
   exists for the sandbox matrix — **musl *and* glibc, aarch64 *and* x86_64** — from
   a maintained, **checksummed** source, ideally a queryable API (PBS for Python,
   nodejs.org/unofficial-builds for Node, the Adoptium API for Java). Prefer
   `.tar.gz` (the gzip stream-push needs no xz). (The Node "musl arm64 needs ≥ 24"
   surprise is why this is step 1.)
2. **Runtime installer** `internal/<lang>` (mirror `internal/node`): resolve the
   exact build for the sandbox's OS/arch/libc; download + **verify SHA256**; extract
   with the Go stdlib and push the tree over `remotefs.FS`; **verify by running the
   interpreter**; idempotent (`AlreadyInstalled`). Provide `Locate(...)` (highest
   matching version) and a `Handler` for the `<lang>.install` verb.
3. **Package manager** `internal/<pkgmgr>` (mirror `internal/pip`/`npm`/`maven`) over
   the neutral `internal/pkg` types, with **both tiers**: **Tier 0** resolves in the
   sandbox against a reachable mirror; **Tier 1 relay** has the controller fetch on
   its network and Popo relay the artifacts in for air-gapped sandboxes. `auto` picks
   relay when no sandbox mirror is configured. Validate the controller-side prereq
   with a clear error (`controller_python`/`controller_npm`/`controller_java`).
   Return whatever the agent needs to *use* the deps (installed-in-place / `NODE_PATH`
   / `classpath`). `Handler` for the `<pkgmgr>.install` verb.
4. **Wire it:** `install <lang> <version>` + `install <pkgmgr> …` commands; register
   both verbs in `buildRegistry`; add config (mirror + controller tool + controller
   repo) and thread it through the session.
5. **Console parity:** add the language to the install form's options; handle it in
   `consoleOps.doInstall` (ensure runtime → install packages); give it a recommended
   **default version**; emit the **Nana verification echo** (runtime version +
   package presence, run *in* the sandbox).
6. **Tests — all of these:** unit tests for platform mapping + resolution parsing; a
   **functional test** (live musl/aarch64 VM) that installs the runtime, resolves a
   real dependency through **both tiers**, and **compiles/runs a program that uses
   it**; **TUI flow test(s)** (teatest) for the new form path; and a **self-contained
   scenario** in `test/scenarios/<lang>/` that builds and runs a real app.
7. **Skill doc:** add the new verbs to the embedded `internal/skill/NANA.md` — the
   sandbox agent learns its available actions there; an undocumented verb is invisible
   to it.
8. **Acceptance demo:** refresh `test/demo/` so the air-gapped agent exercises the new
   language, and re-run `make demo` (see the working agreement).
9. **Docs:** README (commands + verbs), this file's language bullet, and the plan
   (§9 command surface + a decision-log entry).

## Quickstart

```sh
make build            # build ./iceclimber
make test             # unit suite (go test -race ./...)
make sandbox-up       # boot the Lima/Alpine functional sandbox
make test-functional  # black-box E2E against the VM
make tui-functional   # console executor (install/bootstrap) against the VM
make scenario         # full-stack "build a real app" tests per language (test/scenarios/)
make sandbox-down     # tear it down
make demo             # acceptance demo: real Claude agent in an air-gapped VM (DEMO.md)
```

Per-language application scenarios (build + run a real program in the sandbox) live
in [`test/scenarios/`](test/scenarios/), each self-contained with its own README.

Bare **`iceclimber`** launches the **operator console**: it serves the sandbox,
streams live `[POPO]`/`[NANA]` activity, surfaces each approval as an inline modal,
and lets you manage the sandbox from within — `i` opens an install form (pick
**Python**, **JavaScript**, or **Java** and the packages, via huh; the runtime is installed for
you, the package manager pip/npm and tier are derived, version optional), `b`
re-provisions (bootstrap), `s` shows a live status panel (heartbeat/queue/runtimes/
capabilities), `e` manages egress rules (approve/deny/forget), `q` quits. Each
operator action is **verified in the
sandbox** and
echoed into `[NANA]` (the sandbox's voice: `python -V`/`node --version`, a package
presence check, the bootstrap ping/pong) — so `[POPO]` shows what the controller did
and `[NANA]` shows the sandbox confirming it. The TUI-first cockpit (`make
demo-console` wires it for the demo VM). Subcommands stay for scripting/CI;
`iceclimber serve` is the headless path — and bare `iceclimber` with **no terminal**
(CI, pipes) auto-falls back to that unattended serve loop, so command-line operation
keeps working with the TUI present.

Watch a *headless* run unfold with `iceclimber logs -f` (Popo's `[POPO]` activity;
add `--agent-log <file>` for the sandbox `[NANA]` side) — or `iceclimber tui` for a
live split-pane dashboard over the same activity JSONL (the **attach** view, vs the
console's **embed** view). `make demo-logs` / `make demo-tui` wire both for the demo
VM. `serve` prints the same per-request feed on its stdout.

On a terminal, `serve` runs **supervised**: it prompts (Claude-Code-style, with
context) before each install/fetch and you approve inline — `y`/`a`/`n`/`d`.
Inline approval returns the real result in one pass (no out-of-band
pending/approve). `serve --yes` (or any non-TTY/CI run) services everything
unattended; `serve --supervise` forces the prompt without a TTY (reads stdin —
scriptable, and how the functional test drives it).

## Status

**🎉 v1 is complete** — all phases below implemented and verified end-to-end
against a real Alpine/musl sandbox, and the **acceptance demo** (phase 8,
[`DEMO.md`](./DEMO.md)) proves the whole premise with a real Claude agent under a
true air-gap. Remaining work: incremental polish + the v2 backlog (sub-agent/
`web.research`, Tier 2 build, `ExecFS` bulk-transfer, true fleet multiplexing —
plan §0).

- **Phase 1 — done.** CLI skeleton + `probe` (fingerprint OS/arch/libc/root
  viability), verified end-to-end against Alpine.
- **Phase 2 — done.** Maildir protocol + dual `RemoteFS` (`internal/remotefs`:
  SFTP/Exec, conformance at both layers) + `internal/protocol` (envelope,
  dispatcher with id-dedup/recovery/heartbeat, `ping`/`pong`), real `serve` and
  minimal `bootstrap`. Verified E2E on Alpine over both transports.
- **Phase 3 — done.** `python.install` via python-build-standalone
  (`internal/python`): resolve+sha-verify, stdlib extract, transport-agnostic
  push (`RemoteFS` gained `Chmod`/`Symlink`), exec-verify. `install python
  <minor>` + the verb. Verified on Alpine/musl.
- **Phase 4 — done.** `pip.install` Tier 0 (`internal/pkg` + `internal/pip`):
  **resolve (co-resolved, native) → retrieve (per-package)**, tier=mirror + sha256;
  `install pip … --python <minor>` + the verb; `bootstrap` writes `pip.conf`.
  Verified on Alpine/musl vs PyPI. Package management is **multi-language by
  design** (per-manager verbs + neutral types) — see memory.
- **Phase 5 — done.** `pip.install` Tier 1 relay (`internal/pip/relay.go`):
  controller cross-platform `pip download` → relay wheels via `RemoteFS` →
  offline install in-sandbox (tier=relay). `--tier auto|mirror|relay`. Verified on
  Alpine/musl with a C-extension wheel.
- **Phase 6a — done.** `web.fetch` over the **sandbox-exec venue**
  (`internal/webfetch`: curl/busybox-wget over exec, **no Python**; inline/blob
  body) + **SSRF literal-IP floor** + controller-side **audit log**
  (`internal/audit`). `web fetch` CLI + the verb. Verified on Alpine. No exfil
  hole (sandbox's own egress only).
- **Phase 6b — done.** `web.fetch` **controller venue + egress gating**
  (`internal/egress` + `internal/webfetch` controller backend): rewrites, venue
  selection, SSRF-safe dial, allow/deny + pending stores, `pending`/`approve`/
  `deny`, `serve --deny`. Verified on the VM (hold→approve→controller fetch,
  SSRF block, deny).
- **Phase 7 — done (v1 finale).** `NANA.md` skill doc (`internal/skill`, embedded,
  dropped at bootstrap; `skill print`/`skill path`) + `status` (liveness/queue/
  runtimes/capabilities). Verified on Alpine. (Console-script shebang rewriting
  remains deferred — likely moot; pip writes correct shebangs at the runtime's
  final path, and PBS's own scripts are run via `python3 -m`.)
- **Phase 8 — done (acceptance demo).** A real Claude agent in an **air-gapped**
  Alpine VM proves the premise: `test/lima/demo.yaml` (provisioned Alpine + Claude
  Code on musl), `demo-firewall.sh` (egress → only Anthropic's ranges), `test/demo/`
  brief + harness. `make demo` is the headless CI gate (`//go:build demo`,
  subscription token, `ANTHROPIC_API_KEY` emptied); `make demo-live` is the
  operator-approved walkthrough. Validated end-to-end: the agent relayed in Python
  3.12.13 + `rich`, fetched `xkcd` through the gated controller venue, and rendered
  it. See [`DEMO.md`](./DEMO.md). (glibc/Ubuntu variant parked.)
- **Node/npm — done (2nd language).** `internal/node` (mirror of `internal/python`)
  installs a portable Node (glibc nodejs.org/dist, musl unofficial-builds; both
  `.tar.gz`, no xz dep); `internal/npm` (mirror of `internal/pip`) installs globally
  into the runtime prefix and returns a `NODE_PATH` — Tier 0 (sandbox npm vs a
  registry) + Tier 1 relay (controller npm → relay the `node_modules` tree).
  `install node`/`install npm`, the `node.install`/`npm.install` verbs. Verified on
  the aarch64/musl VM (both tiers, `require()` via `NODE_PATH`). Pure-JS only
  (native addons = Node Tier 2, deferred); musl arm64 needs Node ≥ 24.
- **Java — JDK runtime done (3rd language).** `internal/java` (mirror of
  `internal/node`) installs a portable **Temurin JDK** resolved via the Adoptium
  API (musl = os `alpine-linux`, glibc = `linux`; `.tar.gz`, SHA256-verified, no xz
  dep); `install java <version>` + the `java.install` verb. Verified on the
  aarch64/musl VM: installs JDK 21, runs `java`/`javac`, and compiles+runs a program
  (single-file launch). **Dependencies — Tier 0 done:** `internal/maven` resolves
  Maven coordinates via **Coursier** (25 kB launcher relayed in, run on the installed
  JDK) into a **classpath** the agent runs with `java -cp`; `install maven … --java
  VER` + the `maven.install` verb. **Tier 0** runs in the sandbox; **Tier 1 relay**
  has the controller's java resolve + download the JARs (`controller_java`) and Popo
  relays them in — trivially correct since JVM bytecode is platform-independent.
  `auto` → relay when no sandbox mirror is set (air-gap default). Both tiers verified
  on the VM (Guava + transitive → compile+run; Tier 1 against the relayed classpath).
  **Full parity** (per "Adding a language"): wired into the console install form +
  executor with the Nana echo, a teatest flow, and a self-contained scenario
  (`test/scenarios/java/`: fetch → JDK → Gson → compile+run).
