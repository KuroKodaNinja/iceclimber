# ice-climber — design document

Living design doc for `iceclimber`, a Go CLI for operating a Claude agent running in YOLO mode inside a sandbox it can't otherwise provision (Python, packages, web access), over an SSH-only link, with zero assumptions about the sandbox's shell or installed tooling.

Status: **v1 implemented and verified end-to-end** against a real Alpine/musl sandbox (all phases in §12 ✅). Scope was split into a tight **v1** and a **v2** backlog (§0); the design thinking is kept in full. Remaining work is incremental polish + the v2 backlog (sub-agent/`web.research`, Tier 2 build, `ExecFS` bulk-transfer, true fleet multiplexing).

---

## 0. Scope — the v1 line

The design below is kept in full; it is **not** all v1. To honor "ship the minimum that solves the problem," v1 is cut as tightly as it can be while still being a real tool, and the rest is parked as **v2** — designed enough not to be re-derived later, explicitly out of the first build. Every parked item names its own re-entry trigger ("build this when X actually happens"), so v2 is demand-driven, not speculative.

**In v1 (the tight cut):**

- `probe` + `bootstrap` — fingerprint, install-root selection, tree creation, smoke test
- Maildir protocol end-to-end: `ping`, `python.install`, `pip.install`, `web.fetch`
- `RemoteFS` with `SFTPFS` + `ExecFS` and the conformance suite
- `python.install` via python-build-standalone (absolute-path contract)
- `pip.install` Tier 0 (mirror, remote-exec) and Tier 1 (relay)
- `web.fetch` with venue routing, the SSRF floor, **egress gating + fetch rewrites (§6.1)**, audit log
- `NANA.md`

**Parked for v2 (thinking preserved in place, marked `v2:`):**

| Parked | Why not v1 | Re-entry trigger |
|---|---|---|
| Tier 2 build-on-controller (§5) | Reimplements cibuildwheel for the least-likely path; most packages ship manylinux wheels | First real package with no wheel anywhere *and* not in the mirror. v1 fails loudly: "add it to your mirror." |
| `web.research` sub-agent + Tier 3 (§4.5) | The sandbox is already a Claude agent; this is a second product | A concrete resolution case where surfacing to the operator proves insufficient |
| Fleet design (§8) | "Design for it now" is the unrequested-flexibility anti-pattern | A second sandbox that one-process-per-sandbox can't serve. v1 keeps only `sandbox_id` namespacing. |
| `ExecFS` bulk-transfer wire protocol (§6) | Hardest sub-problem, narrow trigger | A box with SFTP disabled *and* needing Python. v1 requires SFTP for the one-time Python push (§6); ExecFS carries only small-file protocol traffic. |

---

## 1. Naming

One Go binary, two roles, named after the two Ice Climbers:

- **Popo** — the controller. Runs *outside* the sandbox (confirmed: typically on a laptop, fully outside the corporate network the sandbox sits in). Owns the SSH/SFTP connection, the deterministic dispatcher, the sub-agent fallback, the local package cache.
- **Nana** — the sandbox-side role. Not necessarily a running process — primarily a persona + skill document (`NANA.md`) that the Claude agent follows using whatever tools its own harness already gives it. An *optional* helper binary (`iceclimber nana ...`) can be pushed in as a convenience once Popo confirms the sandbox can execute one, but nothing in the system depends on it existing.

Functional CLI verbs stay plain and unambiguous (`bootstrap`, `serve`, `install`, ...). The Popo/Nana flavor lives in log lines and status output, not in command names.

---

## 2. Architecture overview

```
 Public internet                              Internal mirror (Artifactory/Nexus/etc.)
        |                                                    |
        v                                                    v
+--------------------------+                    +--------------------------+
| Popo (controller)        |   SSH / SFTP        | Nana (sandbox)           |
|  - package cache         | <==== requests ==== |  - outbox / inbox        |
|  - sub-agent fallback    | ====  results  ====>|    (maildir-style)       |
+--------------------------+                    |  - runtime tree           |
                                                  +--------------------------+
```

- Popo can reach the public internet but **not** the internal mirror (it sits outside the corporate network).
- The sandbox can reach the internal mirror and other company-approved endpoints directly, but has no general internet egress and no way to fetch/install anything itself.
- Requests flow sandbox → controller via `outbox`; results flow controller → sandbox via `inbox`. Both directions ride the same SSH connection.

### Key architectural decision: no daemon required inside the sandbox

The protocol only requires Nana to have *some* file read/write capability — true of essentially any code-capable agent harness. Popo does all polling via its own SSH session (SFTP or exec, see §6). A binary inside the sandbox is a convenience, never load-bearing.

**Scoping boundary (explicit):** actually *running* installed software still requires Nana's harness to expose some execution primitive that resolves paths against the same filesystem Popo populates. If a harness only exposes a fully isolated "run code" tool unconnected to that filesystem, no installation strategy on Popo's side can fix that — that's a harness limitation outside this project's scope, not a bug to work around.

### Not a real chroot

No privileged operations are assumed. The "chrootable" instinct is implemented as a self-contained, relocatable prefix tree (closer to Homebrew/pyenv than `chroot(2)`), addressed by **absolute paths**, not PATH/profile-file modification — non-interactive exec per tool-call means we can't rely on `.bashrc`/`.profile` being sourced, and we don't know which shell (if any specific one) is in play.

---

## 3. Directory layout

### Sandbox side (`$ICECLIMBER_ROOT`, chosen during bootstrap, see §7)

```
$ICECLIMBER_ROOT/
  protocol/
    outbox/
      tmp/            # Nana writes here first
      new/            # atomic rename in — Popo watches this
      cur/            # Popo renames here on pickup (rename = lock)
    inbox/
      tmp/            # Popo writes here first
      new/            # atomic rename in — Nana watches this
      cur/            # Nana renames here after consuming
    blobs/
      <sha256>        # content-addressed: wheels, fetch bodies, python tarball contents
    heartbeat         # Popo writes current timestamp as CONTENT (not mtime — see §6)
    capabilities.json # Nana self-reports its toolset, once, as its first action
    .popo.lock        # held by `serve` for the duration of the session
  runtimes/
    python/
      3.12.6-x86_64-glibc/
        bin/python3   # absolute-path contract — canonical address for invocation
  state/
    manifest.json     # convenience copy; Popo's local copy is authoritative
    pip.conf          # generated at bootstrap, index-url -> internal mirror if configured
  skill/
    NANA.md
```

Request/response filenames are `<ulid>.json` — ULIDs sort lexically by creation time, so "what's oldest in the queue" is a plain directory listing, no per-file parsing required.

### Controller side (operator-owned, never written by Nana)

```yaml
# iceclimber.yaml
sandbox_id: my-sandbox-1
ssh: { host: ..., user: ..., identity_file: ... }
remote_root: /home/agent/.iceclimber     # confirmed/auto-set during probe

network:
  allowed_domains:
    - pattern: "artifactory.corp.internal"
      reachable_from: sandbox
      role: package_mirror               # -> pip.conf index-url points here
    - pattern: "docs.corp.internal"
      reachable_from: sandbox
  # Controller-venue fetches are gated, not allow_and_log (§6.1, supersedes decision #7):
  #   no match in the persistent allow-list -> held in `pending` for operator approval.
  # Sandbox-venue and rewritten-to-mirror fetches ride approved egress and are never held.
  unlisted_domain_policy: gate            # hard SSRF floor below is non-configurable regardless

fetch_rewrites: []                        # §6.1 — redirect/re-venue table (e.g. Maven Central -> Artifactory)

cache_dir: ~/.iceclimber-cache            # wheel/runtime cache, shared across sandboxes by platform fingerprint
approvals_file: ~/.iceclimber/approvals.json  # operator-owned persistent allow-list; never Nana-writable
```

`network.allowed_domains` is a **routing table**, not an access grant — the actual network boundary is enforced outside this tool. It tells Popo (a) which venue to use for a fetch (§6) and (b) whether an endpoint is the package mirror.

A hard-coded floor — blocking link-local/metadata-style addresses (e.g. `169.254.169.254`) and other obvious SSRF/lateral-movement targets — sits underneath `unlisted_domain_policy` and is **not** a config toggle. "Allow and log" governs arbitrary public domains, not the cloud's own metadata service.

---

## 4. Protocol — envelope and schemas

### Envelope

```jsonc
// outbox/new/<ulid>.json (request)
{
  "schema_version": 1,
  "id": "01J9XQK...",
  "type": "pip.install",
  "created_at": "2026-06-21T18:32:00Z",
  "params": { /* type-specific */ }
}
```

```jsonc
// inbox/new/<ulid>.json (response, same id)
{
  "schema_version": 1,
  "id": "01J9XQK...",
  "status": "ok",                 // ok | error | needs_clarification
  "completed_at": "2026-06-21T18:32:04Z",
  "result": { /* present if status=ok */ },
  "error": { "code": "...", "message": "...", "retryable": false },
  "clarification": { "question": "..." }
}
```

`status` describes whether Popo *successfully serviced the request*, not whether every sub-item inside it succeeded — e.g. a `pip.install` batch where 2 of 5 packages fail is still `status: ok`, with per-package failures inside `result`. `status: error` is reserved for Popo failing to even attempt it (malformed request, missing target runtime, internal exception).

**No `pending` stub is written.** (Decided: heartbeat-only liveness, see §4.7 — simpler, revisit only if it proves confusing in practice.) Absence of a response file means "still in progress." Nana polls with backoff and separately watches heartbeat staleness.

**Delivery semantics — at-least-once, deduped to effectively-once (v1).** The transport can drop or double-deliver, so:

- **Crash recovery.** The `new → cur` rename is the pickup-lock, but a request sitting in `cur/` when Popo dies (picked up, no response written) is orphaned. `serve` sweeps `cur/` on startup: any entry without a matching response in `inbox/` is re-processed (or written a `status: error` if non-idempotent — see below).
- **Dedup by `id`.** If an inbox response is lost and Nana re-submits the same logical request, Popo dedups on the envelope `id`: a request whose `id` already has a durable response replays that response instead of re-executing. `python.install`/`pip.install` are naturally idempotent; **`web.fetch` with a non-GET method is not** — for those, re-execution on an unconfirmed delivery is unsafe, so a recovered-from-`cur/` non-idempotent request is answered `status: error, code: interrupted_unsafe_retry` rather than silently re-POSTing.
- Responses are written durably (tmp-write + atomic rename, §6) so a half-written response never looks complete.

### 4.1 `ping`
```jsonc
params: {}
result: { "pong_at": "...", "popo_version": "0.1.0" }
```

### 4.2 `python.install` — *implemented (phase 3)*
```jsonc
params: { "version": "3.12" }   // minor version; Popo pins the exact patch
result: { "version": "3.12.13", "path": "/abs/.../bin/python3", "already_installed": false }
```
Popo resolves the exact patch from PBS's `latest-release.json` (tag + `asset_url_prefix`) and the release `SHA256SUMS` — picks the highest-patch `install_only` asset for the sandbox's `<arch>-unknown-linux-<gnu|musl>` triple, verifies its SHA256, and records the pinned version. The tree is extracted with the Go stdlib (no remote `tar`) and pushed over `RemoteFS` (either transport); `bin/python3` is then run over the exec channel to prove it executes (§2 boundary). Verified on Alpine/musl.

### 4.3 `pip.install` (batched) — *Tier 0 implemented (phase 4)*
```jsonc
params: {
  "python_version": "3.12",
  "packages": [{ "name": "requests", "version": "2.32.3" },  // pinned
               { "name": "rich" }]                            // unversioned -> native default
}
result: {
  "installed": [{ "name": "requests", "version": "2.32.3", "tier": "mirror", "sha256": "..." }],
  "failed": [{ "name": "foo", "version": "1.0", "error": "..." }]
}
```
`tier` ∈ `mirror | relay | built | subagent` — the audit trail (§5). **Resolve → retrieve (decision #21):** Popo first co-resolves the whole request against the index (`pip install --dry-run --report`) — native fidelity, exactly as installing a repo's requirements; an unsatisfiable graph fails the request (`resolution_failed`). It then installs each resolved package independently (`--no-deps`) so individual pull failures are per-package. **Versions need not be pinned in the *request*** (unversioned resolves by the manager's default) — but the *resolution* is always recorded with exact versions + sha256, so determinism lives at the resolved layer. (Supersedes the old "always pinned request" stance.)

### 4.4 `web.fetch`
```jsonc
params: { "url": "https://...", "method": "GET", "headers": {}, "body": null }
result: {
  "status_code": 200, "headers": {},
  "venue": "sandbox-exec",          // or "controller" — which side actually made the call
  "encoding": "utf8",
  "body_inline": "...",             // present if under ~16KB
  "body_blob": "blobs/<sha256>"     // present otherwise
}
```
See §6 for venue selection logic. **Phase 6a (implemented):** the **sandbox-exec
venue** — Popo runs curl, or busybox `wget`, *inside* the sandbox over the exec
channel (no Python; web.fetch is language-agnostic). Body returns inline
(utf8/base64) under 16 KB, else as `blobs/<sha256>`. The **controller venue** and
its policy layer (SSRF DNS floor, egress gating, fetch rewrites, pending/approve/
deny) are **phase 6b**.

### 4.5 `web.research` (sub-agent path) — `v2:` parked (§0)

> **v2.** Not in the first build — the thing in the sandbox is already a Claude agent, so a second research agent inside Popo is a separate product. v1 surfaces resolution ambiguity to the operator instead. Schema kept so v2 doesn't re-derive it.
```jsonc
params: { "question": "...", "context": "...", "max_iterations": 5 }
result: { "answer": "...", "sources": [{ "url": "...", "note": "..." }], "iterations_used": 3 }
```
**OPEN** — concrete tool loop, stopping criteria, prompt construction. See §10.

### 4.6 `capabilities.json` (self-reported by Nana once, not a queued request)
```jsonc
// v1 shape — trimmed to fields something actually consumes (decision #14):
{ "has_exec": true, "has_file_write": true }
```
`has_exec` is the **viability gate**: only Nana knows whether its harness exposes an execution primitive that resolves paths against the tree Popo populates (§2 scoping boundary) — Popo's own SSH exec can't answer that. `has_file_write` is required to participate in the protocol at all. Dropped from v1: `has_network_tools` (Popo drives all fetches, §6) and `shell_hint` (ExecFS is pinned to POSIX sh, §6 — nothing branches on it). Re-add only when a consumer appears. *Phase 7: `NANA.md` instructs Nana to write this as its first action, and `status` **reads it where present** — Popo never requires it.*

### 4.7 Liveness — heartbeat by content, not mtime

Popo writes the heartbeat file's **content** (not mtime). This avoids needing any `stat`-equivalent on Nana's side (a portability trap under `ExecFS`, see §6) and works regardless of what Nana's harness tools expose — "read this file" is the only primitive required.

**Content is `<seq> <iso8601>` — a monotonic counter first, timestamp second.** Nana judges liveness primarily on **counter advancement** ("has `seq` increased across my last K polls"), which needs *no* clock synchronization between the two sides. Agent harnesses frequently have skewed or unreliable clocks, so comparing Popo's absolute timestamp against Nana's wall clock would produce false "Popo is down" verdicts. The timestamp is kept for human logs only, not for the liveness decision.

Skill-documented polling schedule: 2s / 5s / 10s / 30s backoff; if `seq` has not advanced across ~the last several polls (≈2 minutes of wall time at the tail of the backoff), stop waiting and surface "Popo appears to be down" rather than polling forever.

---

## 5. Package resolution tiers

Ordered by how much machinery each needs — always try the cheapest first:

- **Tier 0 — internal mirror, direct remote-exec.** *Implemented (phase 4).* Primary path. Popo remote-execs pip *inside* the sandbox against the mirror, using the sandbox's own already-approved egress — resolve (`--dry-run --report`) then per-package `--no-deps` install. No wheel transfer, no relay. Popo's commands pass `--index-url` **explicitly** (robust under non-interactive exec, decision #23); `bootstrap` *also* writes `state/pip.conf` so the agent's ad-hoc pip hits the same mirror. *(Tested against real PyPI as a stand-in mirror; the egress restriction itself is not yet modeled.)*
- **Tier 1 — Popo-side fetch + relay.** *Implemented (phase 5).* For packages the sandbox can't reach. Popo runs the **operator's `python3`** to `pip download --only-binary=:all: --platform <musllinux/manylinux tag(s)> --abi cp<ver> --python-version <ver>` (cross-platform, targeting the probed fingerprint), relays the wheels in via `RemoteFS` to `blobs/wheels-<id>/`, then remote-execs `pip install --no-index --find-links=… --report` in the sandbox (tier=relay, sha256 hashed from the wheels). The foreign-tag risk was **spiked and resolved** (musl-aarch64 wheels download cleanly from macOS, incl. C-extensions). Selected via `--tier relay` or `auto` when no mirror is set (decision #24–25). Sdist-only packages have no wheel ⇒ Tier 2 build (parked v2).
- **Tier 2 — build-on-controller fallback.** `v2:` parked (§0). Compiled extension, no matching wheel anywhere. Popo builds it in a container/VM matching the probed fingerprint, then drops into Tier 1's transfer mechanics. **OPEN** — exact container strategy. *v1 behavior: fail loudly ("no wheel anywhere; add it to your mirror"), don't build.*
- **Tier 3 — sub-agent.** `v2:` parked (§0) — see §4.5. Genuinely ambiguous resolution, or recovery from a Tier 0–2 failure.

Python itself is always relay-based (Tier 1-style transfer) — nothing exists in the sandbox yet that could pull it from anywhere, mirror or not. *Implemented in phase 3.* **Shebang note (decision #20):** PBS's `bin/python3` is relocatable and runs by absolute path with no shebang reliance, so phase 3 needs no rewriting. `pip`-installed console scripts *do* bake an absolute shebang at the install path — that bites in **phase 4** (pip), where entry points get shebang rewriting or `python3 -m` invocation. Deferred there, not here.

**Determinism note:** regardless of tier, the resolved package hash is always recorded in the response (§4.3) — "company-approved" doesn't mean unverified.

---

## 6. Network — fetch venues and transport

### Two fetch venues

Because Popo sits *outside* the corporate network and the sandbox sits *inside* it, fetches need two execution venues, chosen automatically per target rather than guessed:

- **Controller-side**: Popo fetches from its own network. Right for general internet / public resources.
- **Sandbox-side (remote-exec)**: Popo uses its existing SSH exec access to issue the fetch *from inside the sandbox's network position*, then relays the result back through the inbox file. Required for anything only reachable from where the sandbox sits (the internal mirror, internal docs, etc.) — and Popo can do this without Nana's own harness having any network tool, since Popo is driving the SSH exec channel directly.

Sandbox-side fetches use **curl when present, else busybox `wget`** (which does HTTPS, `--header`, `-O`, and `-S` for the status line) — **not** Python (decision #28; web.fetch must not be tied to the Python runtime). A box with neither curl nor wget is reported clearly.

### 6.1 Egress gating & fetch rewrites (v1) — *implemented (phase 6b)*

*Realized in `internal/egress` (policy + rule/pending stores) + `internal/webfetch` (controller venue, SSRF-safe dial) + the `pending`/`approve`/`deny` CLI. Plain `approve <id>` grants host scope (`--remember` for a custom glob); unlisted URLs default to controller-gated; `deny` persists a deny rule; the SSRF floor blocks private/link-local/metadata at dial (rebinding-resistant) and refuses literal blocked IPs up front.*

`web.fetch` via the **controller venue** is a deliberate tunnel through the sandbox's egress isolation: it lets an in-sandbox agent reach the public internet — including arbitrary POST bodies — from Popo's network position. That is the *point* (the sandbox can't egress on its own), but it is also a data-exfiltration path that defeats the sandbox's reason to exist, so it is **gated, not free**. The SSRF floor alone (blocking `169.254.169.254` and friends) does not address this — "the agent can `POST` whatever it scraped to an arbitrary public host" is the larger threat, and it is what the gate is for.

Two mechanisms run **in order**, before any controller-venue fetch:

**1. Rewrite table — redirect/re-venue before gating.** Generalizes pip.conf's `index-url`: a naive public URL is mapped onto the internal mirror that actually serves it, and re-tagged to the venue that can reach it. A request that rewrites onto a sandbox-reachable mirror **needs no approval** — it never leaves the approved network.

```yaml
# iceclimber.yaml
fetch_rewrites:
  - match: "https://repo1.maven.org/maven2/*"
    rewrite_to: "https://artifactory.corp.internal/maven-central/*"
    venue: sandbox            # rewritten target rides the sandbox's already-approved egress
  - match: "https://pypi.org/*"
    rewrite_to: "https://artifactory.corp.internal/pypi/*"
    venue: sandbox
```

Rewrites are operator-owned config (never Nana-writable). Matching is prefix/glob; the trailing `*` captures the path tail and is appended to `rewrite_to`. The audit entry records both the original and rewritten URL so the redirect is never silent.

**2. Approval gate — for fetches that survive rewriting and still aim at the public internet.** Checked against the operator's **persistent allow-list**:

- **match → allow**, audited.
- **no match → held**: the response is `status: needs_clarification`; the request lands in `pending`; Nana polls/backs off (§4.7). The operator then runs:
  - `approve <id>` — one-shot, this request only.
  - `approve <id> --remember <pattern>` — **persists** an allow rule (e.g. `https://docs.python.org/*`); all future matching controller-venue fetches auto-allow. This is the "permanent approval on Popo's side."
  - `deny <id> --reason "..."` — Nana gets `status: error`, `code: egress_denied`.

The allow-list and rewrite table live in **operator-owned state**, never writable by Nana. Sandbox-venue fetches and rewritten-to-mirror fetches are **never held** — they ride approved egress.

This is the concrete trigger §10 flagged as missing: **approval gates controller-venue egress, not "unlisted domains."** It supersedes the old `allow_and_log` default for the controller venue (decision #7).

**Per-verb kill switch (coarse control).** `serve --deny web.fetch` disables the verb entirely. The verb allowlist Popo serves *is* the security boundary: any file in `outbox/new` is treated as fully operator-authorized (file presence is the only authentication — acceptable for a single-operator laptop tool, stated explicitly so it isn't assumed otherwise). The SSRF floor sits underneath everything and no rewrite or allow rule can reach a link-local/metadata target.

### Transport abstraction: `RemoteFS` — *implemented (phase 2)*

```go
// internal/remotefs — all paths absolute; missing path -> errors.Is(fs.ErrNotExist)
type FS interface {
    Mkdir(ctx, path string) error            // mkdir -p / MkdirAll
    WriteFile(ctx, path string, data []byte) error
    ReadFile(ctx, path string) ([]byte, error)
    List(ctx, dir string) ([]string, error)   // sorted basenames; empty dir -> ([],nil)
    Rename(ctx, old, new string) error          // POSIX replace semantics
}
```

*Refined from the original `WriteAtomic(dir,filename,io.Reader)` sketch (decision #16):* the FS exposes primitive `WriteFile` + `Rename`, and the **maildir layer composes atomic delivery** (write to `tmp/`, rename into `new/`) so readers of `new/` never see a partial file. `data` is `[]byte` for now; a streaming `io.Reader` variant arrives with blobs (phase 3+).

Two implementations, chosen at session open based on whether the SFTP subsystem is actually available (it's sometimes disabled even when exec works); `--transport auto|sftp|exec` overrides (the override exists so the functional suite can exercise ExecFS even where SFTP works):

- **`SFTPFS`** — the fast path (`pkg/sftp`; `PosixRename` for atomic replace).
- **`ExecFS`** — built from day one (decided in review, not deferred), for sandboxes with the SFTP subsystem disabled.

A single conformance suite (`remotefstest.RunConformance`) runs against both — locally (host shell + in-process `net.Pipe` SFTP) **and** against the real Alpine VM over both SSH channels — asserting identical behavior (write/read roundtrip incl. NUL bytes, empty-directory handling, missing-path `ErrNotExist`, rename replace+source-gone). Nothing above this layer (probe, bootstrap, the dispatcher) ever knows which implementation is active.

**`ExecFS` command palette (pinned, not allowed to grow ad hoc):** `sh`, `mkdir -p`, `cat`, `mv`, `rm -f`/`rm -rf`, `ls -1`, `wc -c`, `chmod` (octal), `ln -s`, shell redirection, `&&`. No GNU-specific flags, no `stat` (flags differ across GNU/BSD/busybox). `chmod`/`ln -s` were added in phase 3 for the executable bit + symlinks a relocatable interpreter needs (decision #19); `RemoveAll` (`rm -rf`) added in phase 5 (decision #27). A sandbox lacking even this set is out of scope — stated explicitly rather than discovered later.

**Raw streaming over base64.** A non-pty exec channel is a clean byte stream — `cat > path` with content piped directly into the channel's stdin avoids ~33% base64 overhead. Never request a pty (ptys are what mangle bytes). Base64 kept in reserve only as a defensive fallback if some sandbox proves to mangle raw streams.

**Bulk transfer under `ExecFS`.** `v2:` parked (§0). Naively opening one SSH channel per file (e.g. for a Python distribution with thousands of files) is slow — each channel open is its own round trip. Mitigation: open one long-lived non-pty exec channel running `sh`, and stream a small length-prefixed protocol into it ("here's a path, here's N bytes, write them") rather than spawning a process per file. **OPEN** — the wire protocol itself needs careful design during implementation, not just naming here. **v1 sidesteps this:** the only thousands-of-files payload is the one-time Python distribution push, so **v1 requires `SFTPFS` for `python.install`** and uses `ExecFS` only for steady-state small-file protocol traffic (JSON envelopes + occasional blob), where per-file channel cost is irrelevant. The bulk protocol is built only when a box has SFTP disabled *and* needs Python.

**Extraction trick (both transports).** Extract the Python distribution archive **on the controller** (which definitely has tar/zstd) and push the already-extracted directory tree file-by-file, rather than requiring the sandbox to have `tar`/`zstd`/`unxz` itself. A remote-tar fast path can be added later purely as a speed optimization once it's known to matter, never as a requirement.

### Security floor (not configurable)

- Hard block on link-local/metadata-range addresses and obvious internal-pivot targets, regardless of `unlisted_domain_policy`.
- Every `web.fetch`/`web.research` call gets an append-only audit entry: URL, venue, status/size, timestamp. Bodies are hashed rather than stored in full. *Resolved (phase 6a, decision #30):* **controller-side** JSONL (Popo's copy is authoritative, §3), one file per `sandbox_id`, schema `{ts, id, type, url, method, venue, status_code, body_size, body_sha256, outcome}`. Retention is unbounded for now (append-only); rotation is future work.

---

## 7. Bootstrap & probe sequence

1. **Connect & verify channels.** SSH connect, then verify `exec` and `sftp` independently — don't assume one implies the other. If `exec` fails, bootstrap fails immediately; if `sftp` fails, fall back to `ExecFS` (§6).
2. **Fingerprint the sandbox.** OS, arch, libc (multiple independent signals — disagreement ⇒ low confidence), free disk space, an actual write test (not just permission bits) at each install-root candidate, detection of an existing iceclimber tree (idempotent re-bootstrap). Every probe command is plain POSIX `sh` — no bashisms. Low-confidence fields pause bootstrap and ask the operator to confirm rather than guessing.
3. **Choose the install root.** First writable, durable candidate wins (operator-supplied path checked first, then `$HOME/.iceclimber`, then `/opt/iceclimber` as a root-only long shot). **Decided:** if none is writable, bootstrap fails and requires an explicit operator-supplied path — `/tmp` is never used as a silent fallback, since installs that vanish on reboot defeat the point.
4. **Create the tree & drop the skill.** Maildirs, `blobs/`, `runtimes/`, `state/`, `pip.conf` (safe to write before Python exists). `NANA.md` is written into the tree regardless of harness; the operator gets a printed reminder that wiring it into their specific harness's instructions is a manual integration step Popo can't auto-detect.
5. **Smoke test, no agent involved.** Popo writes a synthetic `ping` directly into `outbox/new` itself, runs one dispatch cycle, confirms `pong` lands in `inbox/new`. Isolates "is the plumbing broken" from "is the agent using it correctly."
6. **Report to operator.** Fingerprint, chosen root, channel capabilities, smoke-test result, skill path, any low-confidence flags. Refuses to proceed to `serve` if critical capabilities (exec) are missing.

`probe` (read-only, phases 1-3 minus writes) is a separate command from `bootstrap` (full idempotent setup) — useful as a standalone diagnostic ("is the box still reachable, has disk filled up") without touching anything.

---

## 8. Fleet extensibility — `v2:` parked (§0)

> **v2.** "Design for it now, build for one" is exactly the unrequested-flexibility the general principles warn against. v1 keeps **only** the `sandbox_id` namespacing below (cheap, one directory level) and the cache-by-fingerprint key; it does **not** build worker-pool or multiplexing machinery. v1's fleet story is literally "run N independent `serve --sandbox X` processes," which needs no fleet design — just no global singletons. The rest is preserved here for when a second sandbox actually arrives.

- All of Popo's local state (probe results, request/response logs, in-flight tracking) is namespaced by `sandbox_id`, even with only one configured today.
- The package/wheel **cache** is namespaced differently — by platform fingerprint (arch/libc/python version), not sandbox — since that's what's actually shareable across a future fleet.
- The `serve` loop is one worker per SSH connection, so "N sandboxes" later is "spawn N workers," not a rewrite.
- v1 fleet story: run N independent `iceclimber serve --sandbox X` processes side by side. A true multiplexed daemon is optional future work, not required now.

---

## 9. CLI command surface

```
iceclimber
  init                                   # scaffold iceclimber.yaml
  bootstrap [--sandbox ID] [--force]     # full idempotent setup; --force is destructive, needs confirmation
  probe [--sandbox ID]                   # read-only diagnostic
  serve [--sandbox ID]                   # long-lived watch loop + sub-agent fallback
  status [--sandbox ID]                  # heartbeat age, queue depth, cache size, recent requests

  install python <version> [--sandbox ID]
  install pip <pkg>[==version]... [--python VER] [--sandbox ID]

  logs [--sandbox ID] [--follow] [--type TYPE]
  pending [--sandbox ID]                 # controller-venue fetches held for egress approval (§6.1)
  approve <id> [--remember <pattern>] [--sandbox ID]   # --remember persists an allow rule
  deny <id> --reason "..." [--sandbox ID]

  cache list | prune | gc
  skill print | path
  config show | validate

  nana request <type> --params <json>    # optional sandbox-side convenience binary
  nana capabilities                      # optional sandbox-side convenience binary

  version
```

Global flags: `--config`, `--sandbox`, `--json`, `-v`.

**`install` reuses `serve`'s handler functions directly** (same tiering logic, called synchronously instead of triggered by a file in outbox) rather than going through the maildir round-trip.

**Decided:** `install` and `serve` contend for the same `.popo.lock`. If `serve` holds it, `install` fails fast with a clear message naming the sandbox and `serve`'s PID, rather than building a local control-socket handoff. Revisit if this proves annoying in practice — the socket is the natural foundation for a future fleet dashboard anyway.

---

## 10. Open items — not yet designed

These are named explicitly so they don't get silently assumed during implementation.

**Still open *for v1* (must be designed before/within the build):**

- ~~**`NANA.md` content**~~ — **resolved (phase 7):** authored in `internal/skill/NANA.md` (embedded), covering abstract file/exec actions, the maildir request/response flow, counter-based heartbeat liveness (§4.7), the absolute-path contract, all four verbs, and the capability self-report. Dropped at bootstrap; `skill print` surfaces it.
- ~~**Audit log schema**~~ — **resolved (phase 6a, §6 floor):** controller-side JSONL, `{ts, id, type, url, method, venue, status_code, body_size, body_sha256, outcome}`. Phase 6b extends entries with fetch **rewrites** (original + rewritten URL) and **approval** outcomes (auto-allowed / one-shot / persisted / denied), per §6.1.
- **Approval persistence format** — the `approvals.json` allow-list shape: pattern syntax (shared with `fetch_rewrites` matching), and whether rules can expire. Small; nail down with the audit schema.

**Resolved since last revision:**

- ~~`pending`/`approve`/`deny` trigger~~ — **resolved (§6.1):** approval gates **controller-venue egress** that survives rewriting, not "unlisted domains." `approve --remember` persists a rule.

**Future refinement — generalized platform/target descriptor.** Tier-1 wheel tags (§5) currently derive a *generous* musllinux/manylinux superset from `arch`+libc-*family*. A language-agnostic improvement: capture `Platform{os, arch, libc{family, version}}` in the probe fingerprint (the gap today is the libc **version**) and have each package manager derive its own identifiers from it (pip tags, cargo `<arch>-unknown-linux-<musl|gnu>`, Go `GOOS/GOARCH`, npm `os/cpu/libc`) — optionally overridden by querying the sandbox's own toolchain (`pip debug`, `rustc --print cfg`) when present. The generous superset is correct meanwhile; build this with the second package manager (where the abstraction earns its keep).

**Parked for v2 (§0) — preserved, not designed now:**

- **Sub-agent loop** (`web.research`, Tier 2/3 fallbacks) — native Go against the Messages API with `web_search`; iteration/stopping criteria, prompt construction, partial-progress reporting all undesigned. See §4.5.
- **Tier 2 build environment** — which container/VM strategy mirrors a probed (arch, libc, python version) fingerprint closely enough to produce ABI-compatible wheels. See §5.
- **`ExecFS` bulk-transfer wire protocol** — named in §6; v1 sidesteps it by requiring SFTP for the Python push.

---

## 11. Decision log

| # | Decision | Rationale |
|---|---|---|
| 1 | No daemon required inside the sandbox | Popo polls via its own SSH session; minimizes assumptions about the sandbox |
| 2 | Absolute-path contract, not PATH/profile edits | Non-interactive exec per tool-call can't reliably source rc files |
| 3 | python-build-standalone (astral-sh) for portable Python | Active, relocatable, maintained; confirmed current as of this doc |
| 4 | Maildir pattern (tmp/new/cur) for outbox/inbox | Atomicity and crash-safety for free, transport-agnostic |
| 5 | Tiered package resolution, mirror-first | Popo can't reach the internal mirror itself; sandbox can |
| 6 | Two fetch venues (controller vs sandbox-exec) | Popo and sandbox sit on opposite sides of a network boundary |
| 7 | ~~`unlisted_domain_policy: allow_and_log`~~ → **controller-venue fetches are gated** (held for approval), with persistent operator allow-rules + a rewrite/redirect table; non-configurable SSRF floor underneath | Controller-venue fetch is a tunnel through the sandbox's egress isolation (exfil risk); gate it, but let approval be made permanent and let requests be redirected to internal mirrors (§6.1) |
| 8 | Heartbeat-only liveness, no `pending` stub; content is **`<seq> <ts>`**, liveness judged on counter advancement | Simpler; counter avoids false-downs from clock skew between Popo and an agent harness with an unreliable clock |
| 9 | `ExecFS` fallback built from day one, not deferred | SFTP subsystem disabled is a real, not hypothetical, failure mode |
| 10 | No `/tmp` fallback for install root; fail and ask operator | Ephemeral installs defeat the point |
| 11 | `install` vs `serve`: lock-and-fail-fast, not a control socket | Simpler for v1; socket revisited only if needed |
| 12 | Tight v1 / v2-backlog split; Tier 2, `web.research`, fleet, ExecFS bulk-transfer parked with re-entry triggers | "Ship the minimum that solves the problem" — keep the thinking, don't build it speculatively |
| 13 | At-least-once delivery, deduped by `id` to effectively-once; `cur/` swept on `serve` start; non-idempotent (`web.fetch` non-GET) recovered requests fail rather than auto-retry | Transport can drop/double-deliver; replaying a POST is unsafe |
| 14 | `capabilities.json` trimmed to `has_exec` + `has_file_write` | Drop fields nothing consumes (`has_network_tools`, `shell_hint`); re-add when a consumer exists (§4.6) |
| 15 | Stack: Go 1.26, cobra CLI, SSH host-key verification via known_hosts (no `InsecureIgnoreHostKey`) | Idiomatic Go CLI; secure-by-default transport (washu security floor) |
| 16 | `RemoteFS` is `WriteFile`+`Rename` primitives; atomic delivery composed in the maildir layer (not a `WriteAtomic` FS method) | Keeps `new/` partial-free via `tmp/`→`new/` rename; simpler FS contract proven identical across both transports |
| 17 | Transport auto-selected (SFTP, else Exec) with `--transport` override | Auto for real use; override lets the conformance/E2E suite exercise ExecFS even where SFTP works |
| 18 | `python.install` resolves via PBS `latest-release.json` + `SHA256SUMS`, sha-verified, recording the exact pinned patch | Low-maintenance vs an in-code version table; still deterministic per install (§4.2) |
| 19 | `RemoteFS` gains `Chmod` + `Symlink`; ExecFS palette += `chmod`, `ln -s` | A relocatable interpreter needs an executable bit and symlinks (`bin/python3`→`python3.x`) |
| 20 | python.install push is transport-agnostic (SFTP or ExecFS); shebang rewriting deferred to phase 4 | The abstraction already gives a uniform push; `bin/python3` runs by absolute path, so shebangs only bite once pip installs console scripts |
| 21 | `pip.install` = **resolve (co-resolved, native) → retrieve (per-package)**; requests may be unversioned but the resolution is recorded with exact versions + sha256 | Native resolution fidelity *and* per-package pull attribution; determinism at the resolved layer (supersedes "always-pinned request") |
| 22 | Per-manager verbs (`pip.install`, later `npm.install`…) over manager-neutral types in `internal/pkg`; build one manager at a time | Multi-language by shape without speculative framework (washu simplicity) |
| 23 | Tier 0 points pip via explicit `--index-url` flags; `bootstrap` also writes `state/pip.conf` | Flags are robust under non-interactive exec (§2); pip.conf lets the agent's ad-hoc pip reach the mirror too |
| 24 | Tier 1 = controller cross-platform `pip download` → relay wheels via `RemoteFS` → offline `--no-index --find-links` install; tier=relay, sha256 from the wheels | The sandbox needs nothing; Popo's network does the fetch; the resolve→retrieve shape carries over (retrieval moves to the controller) |
| 25 | Tier selection explicit: `--tier auto\|mirror\|relay` (auto = mirror if `index_url` set, else relay); no auto-fallback yet | Predictable for phase 5; Tier0→Tier1 auto-fallback is a later refinement |
| 26 | Tier 1 requires `python3`+pip on the **controller** (operator's machine), validated with a clear error | The controller is operator-controlled (unlike the sandbox); a self-contained controller-PBS is deferred |
| 27 | `RemoteFS` gains `RemoveAll` (recursive, idempotent — `rm -rf` / sftp recursive); Tier 1 uses it to clean its transient relayed-wheel dir | A generic delete primitive the contract was missing; closes the wheel-litter gap |
| 28 | `web.fetch` sandbox venue uses **curl, else busybox `wget`** — never Python | web.fetch is a language-agnostic capability; tying it to the Python runtime would be wrong (Python is just our first language) |
| 29 | Split phase 6 → **6a** (sandbox venue + audit, no exfil hole) and **6b** (controller venue + SSRF DNS floor + gating + rewrites + approval) | Land the safe, useful mechanism first; the security-critical policy layer is its own focused phase |
| 30 | Audit log = controller-side append-only JSONL per `sandbox_id`; `{ts,id,type,url,method,venue,status_code,body_size,body_sha256,outcome}` | Popo's copy is authoritative (§3); closes the §10 OPEN item |
| 31 | `approve <id>` grants **host scope** (`https://host/*`); `--remember` for a custom glob; URLs normalized to a path so host rules match bare URLs | One OK unblocks a whole site (practical); normalization avoids a re-submit staying held (bug found in 6b) |
| 32 | Unlisted URL → **controller venue, gated** (held for approval); `deny` persists a deny rule checked before allow | The approval flow exists for exactly this; deny must stick across re-submits |
| 33 | Controller SSRF floor blocks **private** too (Popo is outside the corp net), enforced at **dial** time; literal blocked-IP URLs refused up front for both venues | Rebinding-resistant; the floor sits underneath rewrites/allow rules (§6) |
| 34 | `NANA.md` embedded (`go:embed`) and dropped at bootstrap; `status` reads `capabilities.json` if present | The skill doc ships with the binary (always in sync); the v1 cap-stone surfaces liveness/queue/capabilities |

---

## 12. Suggested build phases

**v1:**

1. ✅ **CLI skeleton + probe (fingerprint only, no installs)** — cobra surface (all §9 verbs present; unbuilt ones stub with their phase label), `config`/`init` real, `remote.Runner` SSH boundary (known_hosts-verified, configurable path), `probe` fingerprints OS/arch/libc(multi-signal confidence)/root-viability(real write tests)/existing-tree. Unit-tested at the Runner boundary **and verified end-to-end against a real Alpine (musl/BusyBox) sandbox** (see Testing strategy below) — probe's POSIX-sh commands and musl detection are proven on real BusyBox.
2. ✅ **Maildir protocol end-to-end with `ping`/`pong`, `RemoteFS` (both implementations) + conformance tests; delivery semantics** — `internal/remotefs` (SFTPFS + ExecFS, conformance at both layers), `internal/protocol` (envelope, `tmp/new/cur` maildir with atomic Deliver/PickUp, ULID names, Dispatcher with `id` dedup + `cur/` recovery sweep + counter heartbeat), real `serve --once`/`serve` and minimal `bootstrap` (tree + heartbeat + §7 smoke test). **Verified E2E on Alpine over both transports** — stdin-over-exec and BusyBox `mv` proven on real hardware. *(Deferred to later phases: `.popo.lock` contention, blob streaming, full re-bootstrap UX.)*
3. ✅ **`python.install` via python-build-standalone** — `internal/python` resolves an exact PBS build (`latest-release.json` + `SHA256SUMS`, sha-verified, pinned), extracts with the Go stdlib, and pushes the tree over `RemoteFS` (either transport; `Chmod`+`Symlink` added) then runs `bin/python3` to verify. `install python <minor>` (sync) + the `python.install` verb. **Verified on Alpine/musl** (3.12.13 in ~6s over SFTP, idempotent). *(Shebang rewriting deferred to phase 4; optimized ExecFS bulk push stays v2.)*
4. ✅ **`pip.install` Tier 0 (internal mirror, remote-exec)** — `internal/pkg` (manager-neutral types) + `internal/pip` (resolve via `--dry-run --report`, then per-package `--no-deps` install; tier=mirror + sha256) + `python.Locate`. `install pip <pkg>… --python <minor>` (sync) and the `pip.install` verb; `bootstrap` writes `pip.conf`. **Verified on Alpine/musl** vs PyPI: unversioned `requests` co-resolves 5 deps, each recorded; unknown package → `resolution_failed`. *(Tier 1 relay = phase 5, reuses the resolve stage.)*
5. ✅ **`pip.install` Tier 1 (relay)** — `internal/pip/relay.go`: controller cross-platform `pip download` (operator's python3) → relay wheels via `RemoteFS` → offline `pip install --no-index --find-links --report` in-sandbox (tier=relay, hashed). `--tier auto|mirror|relay`; config `controller_python` + `pip.controller_index_url`. **Verified on Alpine/musl:** a C-extension (`markupsafe`) downloaded cross-platform on macOS imports and runs on the VM. *(Cross-platform-download risk spiked & resolved; sdist-only → Tier 2, parked v2.)*
6a. ✅ **`web.fetch` — sandbox-exec venue + SSRF literal-IP floor + audit log** — `internal/webfetch` (curl/busybox-wget over exec, no Python; inline/blob body) + `internal/audit` (controller JSONL). `web fetch` CLI + the `web.fetch` verb. **Verified on Alpine/musl** (busybox wget HTTPS; large body → blob). *No exfil hole — sandbox's own egress only.*
6b. ✅ **`web.fetch` controller venue + egress gating (§6.1)** — `internal/egress` (rewrites, venue selection, allow/deny + pending stores) + `internal/webfetch` controller backend (`net/http`, SSRF-safe dial blocking private/link-local/metadata) + `pending`/`approve [--remember]`/`deny` + `serve --deny`. Audit gains `rewritten_url`+`decision`. **Verified on the VM:** rewrite re-venue (ungated), hold→approve→controller fetch, SSRF refusal, deny→egress_denied.
7. ✅ **`NANA.md` + `status` (v1 finale)** — `internal/skill` embeds the sandbox-side skill doc (request/response over the maildir, counter-based heartbeat liveness, absolute-path contract, all four verbs, capabilities self-report); `bootstrap` drops it to `skill/NANA.md`; `skill print`/`skill path`; `status` reports liveness + queue + runtimes + the agent's `capabilities.json`. **Verified on Alpine.**

**🎉 v1 is complete** — every phase above is implemented and verified end-to-end against a real Alpine/musl sandbox. Remaining `polish` + `sandbox_id` fleet-namespacing verification is incremental.

**v2 (demand-driven, see §0):** sub-agent router for `web.research` and Tier 2/3 fallbacks; Tier 2 build environment; `ExecFS` bulk-transfer protocol; true fleet multiplexing.

### Testing strategy

Two tiers, run at different frequencies (washu testing guide):

- **Unit** — fast, every change. Mock at the `remote.Runner` boundary; table-driven parsers/detection logic (`internal/probe`, `internal/config`). Run with `go test -race ./...`.
- **Functional (E2E)** — low-frequency, real dependencies. Black-box: the **real binary** drives a **Lima/Alpine** sandbox over SSH (`test/functional/`, `//go:build functional`, `make e2e`). Alpine = **musl + BusyBox** is deliberate — it enforces the "POSIX sh only, no GNU-only flags" discipline (§6, §7) and exercises musl libc detection. Each phase adds its functional test here (phase 2: maildir `ping`/`pong`; etc.), so "test as we build" holds. Network-boundary simulation (no-egress + mock mirror) is deferred to the pip-tier phases (§5), where it first matters.

---

## 13. Prior art — A2A (Agent2Agent), considered & not adopted

A2A (Google, now v1.0 / Linux Foundation, ~150 orgs as of 2026) is an open protocol for **independent, network-reachable agents to discover and delegate to each other**: JSON-RPC 2.0 over **HTTPS**, SSE streaming, push notifications, discovery via a signed **Agent Card** at a well-known HTTP endpoint.

**Not adopted for the Popo ↔ Nana link — transport mismatch with the project's defining constraint.** Every A2A assumption (an HTTP server, a reachable endpoint, a served Agent Card) is precisely what the architecture refuses to require: an **SSH-only** link, a sandbox with **no assumed network listener and no egress**, and a Nana whose only guaranteed primitive is **file read/write** (§2, §4.6). Adopting A2A would re-introduce the exact assumption the maildir-over-SSH design exists to avoid. This is a firm rejection, not a "maybe later."

**Borrowed concepts (no dependency taken):**
- **Agent Card → `capabilities.json` (§4.6).** Same capability-self-description instinct; loosely align vocabulary. Reinforces pruning `capabilities.json` to fields something actually consumes.
- **Task state `input-required` → `needs_clarification`.** Borrow the *naming* for legibility; do **not** adopt A2A's full task state machine (decision #8's "no pending stub, heartbeat-only" is simpler and stays).

**Possible v2 re-entry, controller-side only:** if the v2 `web.research` sub-agent (§4.5) ever delegates to external specialist agents, **Popo** (which has real internet) could act as an A2A *client* — outbound HTTP from the controller, never touching the sandbox link. Noted, not designed.
