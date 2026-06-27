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
- **Commits.** Conventional Commits, atomic and self-contained — each commit
  builds and passes tests on its own. See `.agents/git/commits`.

## Quickstart

```sh
make build            # build ./iceclimber
make test             # unit suite (go test -race ./...)
make sandbox-up       # boot the Lima/Alpine functional sandbox
make test-functional  # black-box E2E against the VM
make sandbox-down     # tear it down
```

## Status

**🎉 v1 is complete** — all phases below implemented and verified end-to-end
against a real Alpine/musl sandbox. Remaining work: incremental polish + the v2
backlog (sub-agent/`web.research`, Tier 2 build, `ExecFS` bulk-transfer, true
fleet multiplexing — plan §0).

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
