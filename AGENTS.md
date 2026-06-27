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

- **Phase 1 — done.** CLI skeleton + `probe` (fingerprint OS/arch/libc/root
  viability), verified end-to-end against Alpine.
- **Phase 2 — next.** Maildir protocol + dual `RemoteFS` (SFTP/Exec) +
  conformance suite + a `ping`/`pong` functional test.
