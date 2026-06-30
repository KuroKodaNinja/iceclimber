# iceclimber

**Operate a Claude agent inside an SSH-only sandbox it can't provision itself.**

A capable agent dropped into a locked-down box — a corp VM, a CI runner, a
hardened container — often can't do the basics: no Python, no package installs, no
outbound network. iceclimber bridges that gap over nothing but an **SSH/SFTP**
link. A controller you run *outside* the sandbox provisions language runtimes
(**Python, JavaScript/Node, Java**), packages, and web data on the agent's behalf —
and **you** stay in the loop, approving each operation.

> **Want to build something with it?** → **[USAGE.md](USAGE.md)** is the
> start-to-finish guide: point Popo at a box, wire your agent, and let it build.

One Go binary, two roles, named after the Ice Climbers:

- **Popo** — the controller. Runs *outside* the sandbox (your laptop). Owns the
  SSH connection, a deterministic request dispatcher, the package cache, and the
  egress policy.
- **Nana** — the sandbox-side agent. Not a daemon — just a persona plus a skill
  document (`NANA.md`) that the Claude agent follows using whatever file-read/write
  and exec tools its harness already has.

```
   your laptop (Popo)                         the sandbox (Nana)
  ┌────────────────────┐    SSH / SFTP       ┌───────────────────────────┐
  │ iceclimber serve   │◀───────────────────▶│ <root>/protocol/          │
  │  • services reqs   │   (no daemon, no     │   outbox/  ← agent writes │
  │  • installs Python │    open ports —      │   inbox/   ← Popo answers │
  │  • relays packages │    just files)       │ <root>/runtimes/python/.. │
  │  • gated web.fetch │                      │ <root>/skill/NANA.md      │
  │  • asks you to OK  │                      │ <root>/popo  ← the client │
  └────────────────────┘                      └───────────────────────────┘
```

The agent talks to Popo with the **`popo` client** (relayed to `<root>/popo` at
bootstrap): `popo <verb> …` over a **maildir-style** request/response tree on the
sandbox filesystem (`tmp`/`new`/`cur`, atomic rename delivery). No listener, no egress,
no extra tooling required inside the sandbox. A harness that can't execute `popo` can
drive the same tree with only file read/write (see `<root>/skill/PROTOCOL.md`).

---

## Build

Requires Go 1.26.

```sh
make build            # → ./iceclimber
make test             # unit suite (go test -race ./...)
```

### Releases

Cross-build distributable binaries (no network; produces `dist/` with tarballs +
`SHA256SUMS`):

```sh
make release                 # version from `git describe`; or: make release VERSION=v0.2.0
```

This ships **iceclimber** for `darwin/{amd64,arm64}` and `linux/{amd64,arm64}`, plus
the sandbox-side **popo** client for `linux/{amd64,arm64}`. You normally don't need
popo separately — every `iceclimber` **embeds both** linux popo clients and relays
the right one in at `bootstrap`, chosen by the *sandbox's* arch (not your machine's),
so a macOS controller drives a linux/amd64 or /arm64 sandbox all the same. (The
standalone popo tarballs are there for manual/offline placement.) To publish a
GitHub release from a tagged commit:

```sh
git tag v0.2.0 && git push --tags
make gh-release VERSION=v0.2.0     # builds, then `gh release create` uploads dist/*
```

---

## Popo side (the controller — you)

### 1. Point at the sandbox

Write an `iceclimber.yaml` describing the SSH connection (or run `iceclimber init`
for a starter):

```yaml
sandbox_id: my-sandbox
ssh:
  host: 10.0.0.5
  port: 22
  user: agent
  identity_file: ~/.ssh/id_ed25519   # optional; falls back to ssh-agent
  known_hosts: ~/.ssh/known_hosts    # host-key verified (no insecure skips)
# remote_root: /home/agent/.iceclimber   # optional; chosen at bootstrap if absent
```

Host keys are verified against `known_hosts` — the SSH transport is secure by
default, with no insecure skips.

#### Corporate networks: `~/.ssh/config`, jumpboxes, passwords

`host` may be a **`~/.ssh/config` Host alias**, not just a literal address. By
default iceclimber resolves it with `ssh -G` (honoring `Match`/`Include`, `User`,
`Port`, `IdentityFile`, …), so the connection details you already maintain for
`ssh` just work — set `use_ssh_config: false` to force a literal direct dial.

- **Jumpbox / bastion** — put a `ProxyJump` (or `ProxyCommand`) on the host in your
  `~/.ssh/config`; iceclimber reaches the sandbox **through it** by delegating the
  jump to the system `ssh` client (multi-hop and bastion 2FA included). There's no
  iceclimber-specific jump setting — it's abstracted into your ssh config. `trust`
  and the console's first-connect modal also route through the jump. (The
  **bastion's** own host key is verified by `ssh` per your `~/.ssh/config` policy —
  iceclimber only enforces the *target* key — so trust your bastions there as usual.)
- **Password / keyboard-interactive** — opt in with `password_auth: true` (and/or
  `keyboard_interactive: true`). Keys and ssh-agent are always tried first; if a
  password is needed you're prompted **no-echo on the terminal**. This works in
  headless mode too (the prompt uses the controlling terminal), so an unattended
  `serve` still authenticates as long as a terminal exists — otherwise use
  ssh-agent or a key. Passwords are never logged or written to disk; one you type
  is held in memory only and **reused for auto-reconnect** (below).
- **Keepalive & auto-reconnect** — `serve` sends an SSH keepalive every
  `keepalive_interval` seconds (default 20) so a corporate firewall/NAT/bastion
  doesn't silently drop an idle connection, and if the link drops anyway, **`serve`
  reconnects on its own** (capped backoff, retrying indefinitely) instead of exiting
  — in both headless mode and the TUI console (whose header shows `◌ reconnecting…`
  while down). A request the agent delivered during the outage is serviced once the
  link returns. With password auth, the password you typed at startup is reused for
  the reconnect (re-prompted only if it stops working).

```yaml
ssh:
  host: prod-sandbox        # a Host alias from ~/.ssh/config (ProxyJump lives there)
  password_auth: true       # prompt for a password if key/agent don't authenticate
  known_hosts: ~/.ssh/known_hosts
  keepalive_interval: 20    # SSH keepalive seconds (0 = default 20; negative disables)
```

(Honoring `~/.ssh/config` requires the OpenSSH client on the controller; without
it, iceclimber falls back to a literal direct dial.)

### 2. Trust the host key

First contact with a sandbox needs its SSH host key on record. Instead of an
out-of-band `ssh-keyscan` (awkward for short-lived, ephemeral sandboxes), do it
from within iceclimber:

```sh
./iceclimber trust                       # shows the fingerprint, asks to confirm
./iceclimber trust --fingerprint SHA256:… # automation: record only if it matches
./iceclimber trust --replace             # the key rotated (rebuilt/reused address)
```

`trust` fetches the key the sandbox offers, prints its SHA256 fingerprint, and
records it in `known_hosts` — never silently: on a terminal you confirm, and
unattended runs must pass `--fingerprint` (verify) or `--yes` (trust the network).
The bare-`iceclimber` console also offers this as a modal on first connect.

### 3. Bootstrap

```sh
./iceclimber bootstrap
```

Fingerprints the sandbox (OS/arch/libc), picks a writable install root, creates the
protocol tree, runs a `ping`/`pong` smoke test, drops `NANA.md` + `PROTOCOL.md`, and
relays the `popo` client to `<root>/popo`. Then wire `NANA.md` into your agent's
instructions (see the Nana side).

Bootstrap also **detects runtimes already on the box** (a corp VM that ships its own
Python/Node/Java) and lets you choose, per language, whether to use them. By default
iceclimber installs its own pinned runtime (`managed`); choose `system` to use the
sandbox's. On a terminal it asks; for unattended/headless runs set it explicitly:

```sh
./iceclimber bootstrap --runtime-source python=system   # use the box's python3
```

…or pin it in the config (`runtimes: { python: { source: system } }`). In `system`
mode, package installs go into an **iceclimber-owned venv under `$ICECLIMBER_HOME`**
(never the system site-packages — sidestepping PEP 668 and write permissions), and
relayed wheels are matched to the discovered interpreter. iceclimber uses what's on
the box and fails clearly if the agent asks for a version that isn't there — it never
changes the system toolchain.

### 4. Serve — the console, or headless

Bare **`iceclimber`** launches the interactive **console**: it serves the sandbox,
streams live activity, and surfaces every approval as a modal you answer in-place —
a split-pane `[POPO]`/`[NANA]` cockpit (`[POPO]` = what the controller did, `[NANA]`
= the sandbox's own voice — the agent's stream plus sandbox-verified confirmations).
You can also drive the sandbox from inside it: **`i`** opens an install form (pick
**Python**, **JavaScript**, or **Java** — the runtime is installed for you; packages and
version are optional, since the agent installs packages as its code needs them),
**`a`** installs or wraps a coding agent (the `nana` wrapper — relay it in, or wrap a
binary already on the sandbox; auth comes from your environment, never typed into the
UI), **`b`** re-provisions (bootstrap), **`s`** shows live status,
**`e`** manages egress rules (approve/deny/forget), **`q`** quits. Each
operator install is **confirmed in the sandbox** (the interpreter's own version
banner, a package presence check) and echoed into `[NANA]`. While an install runs,
the footer shows a **live progress meter** — a spinner, the current phase
(resolving / downloading / transferring / verifying), a bar with %/bytes/ETA for
the transfer (or an `(i/n)` count for pip packages; npm/maven show a phase
spinner), and the **transfer mode** (`· via exec` or `· via sftp`). The `iceclimber install …` CLI shows the same
progress on a terminal (a single updating line; plain phase lines when piped).

```sh
./iceclimber                    # the console (serve + watch + approve + manage)
./iceclimber serve              # headless watch loop (CI/unattended)
```

On a terminal `serve` runs **supervised** — it prompts you to approve each
operation the agent requests, with context, and you approve inline:

```
  ╭─────────────────────────────────────────────
  │ Approve egress · sandbox my-sandbox
  │ web.fetch  GET
  │   url       https://example.com/data.json
  │   via       Popo's network (controller venue)
  │   why       host is not in the allow-list
  │
  │ ⚠ This leaves YOUR machine's network, not the sandbox's.
  ╰─────────────────────────────────────────────
    [y] approve   [a] approve + remember host example.com   [n] deny   [d] deny+remember   [?]
```

`y` allow once · `a` allow + remember · `n` deny · `d` deny + remember. Run
`serve --yes` (or any non-TTY/CI invocation) to service everything unattended.

### Other Popo commands

| Command | What it does |
|---|---|
| `status` | Liveness (heartbeat freshness), queue depth (awaiting + delivered), **health-probed** runtimes (✓ runs / ✗ won't), the agent's capabilities. The console header shows the SSH link and heartbeat health as **separate** signals, so a connected-but-wedged Popo reads as stale, not green |
| `logs -f` | Tail Popo's activity (`[POPO]`) merged with the agent's stream (`[NANA]`, bridged automatically; `--agent-log <file>` overrides) |
| `tui` | A live split-pane `[POPO]`/`[NANA]` dashboard (`[NANA]` bridged automatically; `--snapshot` for one static frame) |
| `pending` / `approve <id>` / `deny <id>` | Async egress approval (when not serving on a TTY) |
| `install python <minor>` · `install pip <pkg> --python <minor>` | Provision Python directly, without the agent. `--pip-arg` (repeatable) passes an allowlisted pip flag through, e.g. `--pip-arg=--index-url --pip-arg=https://download.pytorch.org/whl/cpu` |
| `install node <version>` · `install npm <pkg> --node <version>` | Provision Node/npm directly |
| `install java <version>` | Provision a Temurin JDK (javac bundled) directly |
| `install maven <group:artifact:version> --java <version>` | Resolve JVM deps into a classpath (Coursier) |
| `agent install [claude]` · `agent list` | Relay a coding agent (Claude Code) into the sandbox + configure its subscription token; drops a `$ICECLIMBER_HOME/nana` launcher to start it (auth + NANA.md wired in) |
| `agent wrap [claude] [--bin <path>]` | Wrap an agent binary **already on the sandbox** (pre-baked image / out-of-band install) with the same `nana`/auth/NANA.md launcher — **no relay**. Binary found on PATH by default, or pass `--bin` |
| `web fetch <url>` | Run a fetch yourself (same gating) |
| `skill print` / `skill path` | The `NANA.md` contract (`--protocol` for the raw `PROTOCOL.md`) |

---

## Nana side (the sandbox agent)

Nana's contract is **`NANA.md`** — a short skill doc dropped into `<root>/skill/NANA.md`
at bootstrap (printable with `iceclimber skill print`). It tells the agent to talk to
Popo with the **`popo` client**, which `bootstrap` also relays into `<root>/popo`:

- **`popo help`** lists the actions; **`popo <verb> …`** (e.g. `popo python.install
  3.12`, `popo web.fetch <url>`) builds the request, delivers it, waits, and prints a
  clean result. The agent never hand-writes the protocol or parses JSON.
- **Run installed runtimes by the absolute path `popo` prints** (e.g. `<path> -c
  "print(1)"`), never by bare name.
- **Approvals:** if `popo` exits 2, Popo needs the operator to approve something —
  the agent relays the message and retries.
- **Interactive shells:** `eval "$(<root>/popo shellenv)"` sets `ICECLIMBER_HOME` and
  puts `<root>` on `PATH`, so `popo`/`nana` then run by name (à la `brew shellenv`).

**File-I/O-only fallback.** A harness that *can't execute* `popo` can still drive the
bridge with nothing but file read/write — the raw maildir protocol (envelope →
`outbox/tmp` → rename to `outbox/new`, poll `inbox/new`, heartbeat liveness) lives in
`<root>/skill/PROTOCOL.md` (`iceclimber skill print --protocol`). The agent picks `popo`
when it can exec, the file protocol when it can't.

**To make a real Claude agent be Nana:** wire `NANA.md` into its instructions and let
it operate in the sandbox — or just run **`iceclimber agent install claude`**, which
relays Claude in and drops a `nana` launcher that starts it with NANA.md as its system
context. To learn the protocol by hand, see [`test/PLAYGROUND.md`](test/PLAYGROUND.md).

### What Nana can ask Popo for

| Verb | Provides |
|---|---|
| `ping` | Liveness check (`pong`) |
| `python.install` | A portable Python (python-build-standalone), run by absolute path |
| `pip.install` | Python packages — from a sandbox-reachable mirror (Tier 0) or relayed in by Popo for air-gapped boxes (Tier 1). Accepts allowlisted `extra_args` (index/version-selection flags only — `--index-url`, `--extra-index-url`, `--pre`, `-f`, …; no build-behavior flags, no shell). In **relay** mode an agent `--index-url` directs the **controller's** download to that index — a deliberate capability (PyTorch's wheel index) but not covered by the web.fetch egress gate, so it's an allowlist, not arbitrary passthrough |
| `node.install` | A portable Node.js runtime (npm bundled), run by absolute path |
| `npm.install` | npm packages (Tier 0 mirror / Tier 1 relay); returns a `NODE_PATH` to `require()` them |
| `java.install` | A portable Temurin JDK (javac bundled), run by absolute path |
| `maven.install` | JVM deps (Maven coordinates) resolved via Coursier — Tier 0 mirror or Tier 1 relay; returns a `classpath` to run with `java -cp` |
| `web.fetch` | A URL — via the **sandbox's** own egress (ungated) or **Popo's** network (gated controller venue) |

---

## Security model

- **Egress is gated.** A fetch through Popo's network (the controller venue) is a
  tunnel out of the sandbox's isolation, so it requires your approval — inline at
  the `serve` prompt, or asynchronously via `pending`/`approve`. Approvals/denials
  are **operator-owned** files, never writable by Nana, and persist across runs.
- **Non-configurable SSRF floor.** Private, loopback, link-local, and cloud-metadata
  addresses are refused for both venues, enforced at dial time (rebinding-resistant).
- **Audit log.** Every fetch appends to a controller-side JSONL audit
  (`~/.iceclimber/audit/<sandbox_id>.jsonl`).
- **Secure transport.** SSH host keys are verified against `known_hosts`.

---

## Try it on a real agent

- **Drive it by hand** against a local Lima/Alpine sandbox —
  [`test/PLAYGROUND.md`](test/PLAYGROUND.md).
- **A real Claude agent in a network air-gapped sandbox**, with inline approvals —
  [`DEMO.md`](DEMO.md):
  ```sh
  make demo-up && make demo-live      # supervised serve; approve each op (see DEMO.md)
  make demo-console                   # or the graphical console (bare iceclimber)
  make demo                           # or fully headless, asserts the result
  ```
- **Build a real app in the sandbox** (per-language full-stack scenarios) —
  [`test/scenarios/`](test/scenarios/): `make scenario`.

---

## Documentation

- [`USAGE.md`](USAGE.md) — **start here to build apps**: point Popo at a sandbox,
  wire your agent, run the console, approve as it builds.
- [`DEMO.md`](DEMO.md) — the air-gapped real-agent acceptance demo.
- [`test/PLAYGROUND.md`](test/PLAYGROUND.md) — drive the protocol by hand.
- [`test/scenarios/`](test/scenarios/) — per-language "build a real app" scenarios.
- [`AGENTS.md`](AGENTS.md) — contributor guide, working agreement, build phases.
- [`ice-climbers-plan.md`](ice-climbers-plan.md) — the design source of truth
  (architecture, protocol, decision log).

## License

[MIT](LICENSE).

**Status:** v1 plus a run of increments are complete and verified end to end against
a real Alpine/musl sandbox — including a real Claude agent under a true network
air-gap. What's shipped: the maildir protocol + gated `web.fetch`; **three languages**
— **Python** (pip), **JavaScript/Node** (npm), and **Java** (Maven/Coursier) — each
with runtime install + packages over **Tier 0 (mirror)** and **Tier 1 (relay)**; and
the **TUI-first operator console** (bare `iceclimber`: serve embedded, inline
approvals, install/bootstrap forms, a live status panel, and egress-rule management).
The [acceptance demo](DEMO.md) re-proves the premise across all three languages. Next
up is **multi-sandbox** (a fleet list/switcher); other extensions (sub-agent
`web.research`, a Tier 2 build environment) are parked as demand-driven work (plan §0;
decisions #46–#54).
