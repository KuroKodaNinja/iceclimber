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

- **Dogfood every change in a test — against the contract, not the implementation.**
  No change ships without a test that exercises it the way a real consumer uses it
  (the operator, the agent following `NANA.md`, or the demo), and that asserts
  against the **documented contract / an independent source of truth** — never just
  round-trips the implementation's own assumptions. A test that mirrors the code's
  internals can be *self-consistent with the bug* and prove nothing. **Cautionary
  case (#59):** `web.fetch` wrote response blobs to `$ICECLIMBER_HOME/blobs` while `NANA.md`
  (correctly) told the agent to read `$ICECLIMBER_HOME/protocol/blobs`; the functional test
  read the blob via `path.Join(root, body_blob)` — the same wrong path the writer
  used — so it passed for months while a real agent couldn't find the file. The fix
  was to assert against the spec'd location and tie writer + published reference to
  one canonical accessor (`Tree.Blobs()`/`BlobRef`). When a doc/spec names a path,
  flag, or field, a test must check the code matches the doc.
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

### Pre-commit/push procedure (definition of done)

Run this every time, before staging — committing and pushing is the *last* step,
never the first:

1. **Assess the work & clean up.** Re-read the diff as a whole. Remove dangling
   things the change orphaned — dead code, stale glue/fixtures, superseded demo
   wiring, leftover scaffolding, commented-out blocks. Nothing the change made
   obsolete should survive.
2. **Exercise every operating mode the change touches.** Confirm each still works,
   not just the one you developed against:
   - **headless / non-interactive** *and* **TUI/console** operation;
   - **popo-binary** *and* **no-popo (file-I/O-only / `PROTOCOL.md`)** modes;
   - any other flow this change can reach (CLI subcommands, both transports
     sftp/exec, supervised vs `--yes`, etc.).
3. **Cover any untested flow end to end.** If a use case or flow above lacks a full
   E2E/functional test (or a `teatest` flow for TUI behaviour), **write it now** —
   per "Dogfood every change" and "Test as we build". A mode without a test isn't
   validated, it's assumed.
4. **Sync all documentation.** README, USAGE, AGENTS, DEMO, and the plan's decision
   log must match what we actually wrote — flags, paths, behaviour, and a new
   decision entry where one is warranted. When a doc names a path/flag/field, a test
   should pin it (#59).
5. **Validate green — everything, no matter what.** Before any push, run the
   **complete** suite, not a subset chosen by what the change "touched": `make test`
   (unit, `-race`), the functional suites (`make e2e` / `make test-functional` /
   `make tui-functional`), **and** the acceptance demo
   (`set -a; . .demo/token.env; set +a; make demo` — it silently skips without the
   token, so confirm it actually ran). No skipping the demo because a change "looks
   unrelated"; the gate to GitHub is the whole thing passing.
6. **Only then commit & push.** Stage files explicitly (never `git add -A`; keep
   `.claude/` untracked), atomic Conventional Commits, no AI-attribution trailer
   unless explicitly asked.

### Quality pass (after a complex feature or refactor)

The per-change gate above isn't a full audit. When wrapping up a **complex feature
or refactor** (multi-commit, new subsystem, security-sensitive surface like the SSH
transport), run a dedicated quality sweep *before* the final push — ideally as
**parallel focused reviews, one per dimension**, then synthesize, fix the real
findings (note nits, don't necessarily fix), re-run the gate on what you touched,
and ship via the procedure above. The four dimensions:

1. **Security** — secret handling (never logged/argv/env/disk), authn/authz, injection
   (args/shell/paths), subprocess hygiene (reap/kill/env/stderr), file perms,
   secure-by-default invariants preserved (no `InsecureIgnoreHostKey` etc.). State the
   threat model (what's operator-owned vs reachable from the sandbox/agent).
2. **Test coverage** — untested branches/edge/error paths; and hunt the **#59
   anti-pattern**: tests that assert the implementation's own assumptions, or whose
   *name* claims a contract the body doesn't exercise (a misnamed test is worse than
   none).
3. **Code quality & cleanup** — dead code / orphans the change left behind, Go idioms,
   simplicity ("would a senior engineer call this overcomplicated?"), correctness
   smells, over-engineering vs gaps.
4. **Docs / spec consistency** — every doc/scaffold-named flag, path, or field exists
   and behaves as documented (the #59 rule); decision-log accurate; no stale wording.

This sweep found, on the corporate-SSH feature: a latent tilde-expansion bug, a
misnamed precedence test (#59), option-injection hardening, and several real coverage
gaps — none caught by the per-change gate alone. Scale it to the work: a couple of
reviewers for a moderate feature, all four for a security-sensitive or sprawling one.

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
   package presence, run *in* the sandbox). Pass the threaded `progress.Func` so the
   install reports live progress (decision #66) — the runtime installer reports byte
   transfer; package managers report per-package/phase steps.
6. **Tests — all of these:** unit tests for platform mapping + resolution parsing; a
   **functional test** (live musl/aarch64 VM) that installs the runtime, resolves a
   real dependency through **both tiers**, and **compiles/runs a program that uses
   it**; **TUI flow test(s)** (teatest) for the new form path; and a **self-contained
   scenario** in `test/scenarios/<lang>/` that builds and runs a real app.
7. **Client + skill docs:** teach the `popo` client the verb — extend the `verbs`
   table + `buildParams` (and `printResult` if the result shape is new) in `cmd/popo`
   (that's the agent's `popo help`) — and add it to the verb table in
   `internal/skill/PROTOCOL.md` (the raw-protocol/file-I/O-only reference). NANA.md
   itself stays minimal and points at `popo help`, so it needs no per-verb edit.
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
make release          # cross-build dist/ tarballs (iceclimber darwin/linux + popo linux) + checksums
make gh-release       # build + publish dist/* to a GitHub release (gh; from a tagged commit)
```

Per-language application scenarios (build + run a real program in the sandbox) live
in [`test/scenarios/`](test/scenarios/), each self-contained with its own README.

Bare **`iceclimber`** launches the **operator console**: it serves the sandbox,
streams live `[POPO]`/`[NANA]` activity, surfaces each approval as an inline modal,
and lets you manage the sandbox from within — `i` opens an install form (pick
**Python**, **JavaScript**, or **Java**, via huh; the runtime is installed for
you, the package manager pip/npm and tier are derived; packages and version are
optional — blank installs just the runtime, since the agent installs packages as
its code needs them), `b`
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

Watch a *headless* run unfold with `iceclimber logs -f` (Popo's `[POPO]` activity +
the sandbox `[NANA]` stream — both with no flag; a serving Popo bridges the agent
stream to the controller `agent.log` the views default to) — or `iceclimber tui` for a
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
`web.research`, Tier 2 build, true fleet multiplexing — plan §0). The `ExecFS`
bulk-transfer shipped (decision #55): runtime trees push in one `tar -xf -` exec,
so all languages install over either transport (no SFTP-only constraint).
`iceclimber trust` records a sandbox's SSH host key in-CLI (decision #56) — the
ssh-keyscan replacement for ephemeral boxes; keys are never trusted silently
(terminal confirm, or `--fingerprint`/`--yes` for automation), and the console
offers it as a first-connect modal. Host-key primitives live in
`internal/remote/hostkey.go` (`FetchHostKey`/`CheckHostKey`/`RecordHostKey`); an
unknown/changed key surfaces as a typed `remote.HostKeyError`.
**Corporate SSH** (decision #65): `ssh.host` may be a `~/.ssh/config` Host alias —
by default `remote.Dial` resolves it with `ssh -G` (honoring the operator's config)
and reaches the target **through any `ProxyJump`/`ProxyCommand`** by delegating the
jump to the system `ssh` client over a subprocess `net.Conn` (so jumpboxes/2FA need
no iceclimber config). Opt-in `password_auth`/`keyboard_interactive` prompt no-echo
on `/dev/tty` (works headless too). All four dial sites funnel through `dialConfig`
→ `remote.buildDialPlan`; `ResolveTarget` keys host-key trust on the resolved name.
`iceclimber agent install [claude]` installs a coding agent into the sandbox
(decision #57): `internal/agent` downloads the agent's **per-platform** package
(e.g. `@anthropic-ai/claude-code-linux-arm64-musl`) on the controller and **relays
the self-contained native binary in** (via `remotefs.PushTarGz`), so it works on a
fully air-gapped sandbox — no on-target npm, no Node. It writes a 0600 auth env
file: subscription token only — an API key is refused, `ANTHROPIC_API_KEY` is
blanked, the token is never logged. New agents are just another `agent.Descriptor`
(npm-prefix + platform-suffix mapping + binary path + token/env). It also writes a
**`$ICECLIMBER_HOME/nana` launcher** (operator runs it *in* the sandbox from any dir): a generic
dispatcher picks the agent (the sole one, or `nana <agent>` when several) and execs
a per-agent `run` script that sources the env and launches the harness with `NANA.md`
as persistent system context (`--append-system-prompt`) plus passthrough args. The
agent-specific launch recipe is baked from the `Descriptor` at install time, so the
sandbox scripts stay generic. The demo dogfoods it (`demo-agent.sh` → `$ICECLIMBER_HOME/nana`).
Run headless (a print flag like `-p`, or non-tty), `nana` mirrors the agent's stream to
`$ICECLIMBER_HOME/agent/<name>/session.log` (interactive runs keep their tty, not mirrored). The
**serving process bridges** those logs to a controller-side `agent.log`
(`bridgeAgentLog`→`pollAgentLogs` runs in the console *and* headless `serve`, since both
hold the SSH session), and the console, `tui`, and `logs` all **default `--agent-log` to
that file** (`agentLogPath`) — so `[NANA]` shows the agent with **no flag** (decision #60).
`--agent-log` stays an explicit override. On a **print run** (a `-p` flag present) with
no explicit `--output-format`, the `run` script auto-injects the descriptor's
`StreamArgs` (`--output-format stream-json --verbose`) so `[NANA]` shows tool calls, not
just the final summary — gated on `-p` so `--version`/diagnostics stay clean and a
caller's own `--output-format` always wins (decision #63). The demo dogfoods the whole
path (`demo-agent.sh` → `$ICECLIMBER_HOME/nana -p` with **no** stream flags, relying on injection;
`make demo-logs/tui/console` pass no flag).

The agent talks to Popo through the **`popo` client** (`cmd/popo`, decision #61), not
by hand-crafting the maildir protocol: `popo <verb> …` builds/delivers/polls/parses
and prints a clean result. It reuses the leaf **`internal/wire`** package (envelope,
`Tree`, `NewID` — no FS deps; `protocol` re-exports it via aliases) so client and
dispatcher share one wire format. It's cross-compiled `CGO_ENABLED=0` (one static
binary per GOARCH, musl+glibc), `go:embed`'d in `internal/popobin` (built by `make
popo-bins`; the functional `TestMain` builds it too), and **bootstrap relays it to
`$ICECLIMBER_HOME/popo`**. So **NANA.md is minimal** (system-prompt-sized: "run `popo help`,
then `popo <verb>`") — the raw protocol lives in **PROTOCOL.md** (also dropped at
bootstrap, not in the system prompt) as the **file-I/O-only fallback** for harnesses
that can't exec. Adding a verb to `popo`: extend the `verbs` table + `buildParams`
(and `printResult` if the result shape is new).

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
