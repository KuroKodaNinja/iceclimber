# iceclimber

**Operate a Claude agent inside an SSH-only sandbox it can't provision itself.**

A capable agent dropped into a locked-down box — a corp VM, a CI runner, a
hardened container — often can't do the basics: no Python, no package installs, no
outbound network. iceclimber bridges that gap over nothing but an **SSH/SFTP**
link. A controller you run *outside* the sandbox provisions Python, packages, and
web data on the agent's behalf — and **you** stay in the loop, approving each
operation.

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
  │  • asks you to OK  │                      │  (a Claude agent reads it)│
  └────────────────────┘                      └───────────────────────────┘
```

The agent and Popo communicate through a **maildir-style** request/response tree on
the sandbox filesystem (`tmp`/`new`/`cur`, atomic rename delivery). No listener, no
egress, no extra tooling required inside the sandbox — only the ability to read and
write files, plus exec to *run* what gets installed.

---

## Build

Requires Go 1.26.

```sh
make build            # → ./iceclimber
make test             # unit suite (go test -race ./...)
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
default.

### 2. Bootstrap

```sh
./iceclimber bootstrap
```

Fingerprints the sandbox (OS/arch/libc), picks a writable install root, creates the
protocol tree, runs a `ping`/`pong` smoke test, and drops `NANA.md`. Then wire that
skill doc into your agent's instructions (see the Nana side).

### 3. Serve

```sh
./iceclimber serve              # watch loop: heartbeat + service requests
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
| `status` | Liveness (heartbeat), queue depth, installed runtimes, the agent's capabilities |
| `logs -f [--agent-log <file>]` | Tail Popo's activity (`[POPO]`) merged with the agent's stream (`[NANA]`) |
| `pending` / `approve <id>` / `deny <id>` | Async egress approval (when not serving on a TTY) |
| `install python <minor>` · `install pip <pkg> --python <minor>` | Provision directly, without the agent |
| `web fetch <url>` | Run a fetch yourself (same gating) |
| `skill print` / `skill path` | The `NANA.md` contract |

---

## Nana side (the sandbox agent)

Nana needs only **file read/write** (plus exec to run installed binaries). The full
contract is `NANA.md`, dropped into `<root>/skill/NANA.md` at bootstrap and printable
with `iceclimber skill print`. In short, the agent:

1. **First action** — writes `protocol/capabilities.json` = `{"has_exec":true,"has_file_write":true}`.
2. **Requests** — builds an envelope `{schema_version, id, type, created_at, params}`,
   writes it to `outbox/tmp/<id>.json`, then **renames** into `outbox/new/` (never
   writes `new/` directly).
3. **Responses** — polls `inbox/new/<id>.json` (same id) with backoff; reads the result.
4. **Liveness** — judges "is Popo alive" on the heartbeat **counter advancing**, not
   its timestamp (no clock sync needed).
5. **Absolute paths** — runs installed binaries by the path the result returns (e.g.
   `runtimes/python/<ver>/bin/python3`), never relying on `PATH`/rc files.

**To make a real Claude agent be Nana:** give it the output of `iceclimber skill
print` as part of its instructions and let it operate inside the sandbox. Any agent
that can read and write files can be Nana — Claude is just the one we drop in. To
learn the protocol by hand first, see [`test/PLAYGROUND.md`](test/PLAYGROUND.md).

### What Nana can ask Popo for

| Verb | Provides |
|---|---|
| `ping` | Liveness check (`pong`) |
| `python.install` | A portable Python (python-build-standalone), run by absolute path |
| `pip.install` | Packages — from a sandbox-reachable mirror (Tier 0) or relayed in by Popo for air-gapped boxes (Tier 1) |
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
  make demo-up && make demo-live      # supervised; approve each op (see DEMO.md)
  make demo                           # or fully headless, asserts the result
  ```

---

## Documentation

- [`DEMO.md`](DEMO.md) — the air-gapped real-agent acceptance demo.
- [`test/PLAYGROUND.md`](test/PLAYGROUND.md) — drive the protocol by hand.
- [`AGENTS.md`](AGENTS.md) — contributor guide, working agreement, build phases.
- [`ice-climbers-plan.md`](ice-climbers-plan.md) — the design source of truth
  (architecture, protocol, decision log).

**Status:** v1 is complete — `probe`, the maildir protocol, `python.install`,
`pip.install` (mirror + relay), gated `web.fetch`, supervised `serve`, observability
— all verified end to end against a real Alpine/musl sandbox and a real Claude agent
under a true network air-gap. Sophisticated extensions (sub-agent `web.research`,
build-on-controller wheels, fleet multiplexing, a graphical TUI) are parked as
demand-driven v2 work (see the plan's §0).
