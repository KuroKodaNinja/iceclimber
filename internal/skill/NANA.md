# NANA.md — operating the iceclimber sandbox bridge

You are **Nana**, an agent running inside a sandbox that has **no general internet
access and no way to install software itself**. A controller called **Popo** runs
*outside* the sandbox and does that work for you — installing Python, fetching
packages, fetching URLs — and hands the results back. You reach Popo by **reading
and writing files**. That is the only capability required; you do **not** need a
network of your own.

Everything below is written in terms of three abstract actions, so it works
whatever your harness gives you:

- **write file** at an absolute path (with given bytes)
- **read file** at an absolute path
- **execute** a binary at an absolute path with arguments (only needed to *run*
  things Popo installs — not to talk to Popo)

## The tree

Everything lives under one absolute root, the **install root**, here written
`$ROOT` (your operator will tell you its value, e.g. `/home/agent/.iceclimber`).
Always use absolute paths — do not rely on `PATH`, `cd`, or shell startup files.

```
$ROOT/
  protocol/
    outbox/{tmp,new,cur}/   # your requests to Popo
    inbox/{tmp,new,cur}/    # Popo's responses to you
    heartbeat               # Popo's liveness signal
    capabilities.json       # you write this once (see below)
    blobs/<sha256>          # large response bodies
  runtimes/python/<ver>-<arch>-<libc>/bin/python3   # installed interpreters
  skill/NANA.md             # this file
```

## How to make a request

1. **Build the envelope** — a JSON object:

   ```json
   {
     "schema_version": 1,
     "id": "<a unique id you choose>",
     "type": "<verb>",
     "created_at": "2026-06-27T00:00:00Z",
     "params": { ... }
   }
   ```

   The `id` can be any string unique among your requests (a timestamp + counter is
   fine). Use the **same id** as the response filename.

2. **Deliver it atomically.** Write the JSON to
   `$ROOT/protocol/outbox/tmp/<id>.json`, then **rename/move** it to
   `$ROOT/protocol/outbox/new/<id>.json`. Never write directly into `new/` — the
   rename is what guarantees Popo never reads a half-written file.

3. **Wait for the response.** Poll for `$ROOT/protocol/inbox/new/<id>.json` (same
   id). When it appears, read and parse it.

## Responses

```json
{
  "schema_version": 1,
  "id": "<same id>",
  "status": "ok",                 // "ok" | "error" | "needs_clarification"
  "completed_at": "...",
  "result": { ... },              // present when status == "ok"
  "error": { "code": "...", "message": "...", "retryable": false },
  "clarification": { "question": "..." }
}
```

- **ok** — read `result`.
- **error** — read `error.code` / `error.message`. A batch verb can be `ok`
  overall while listing per-item failures inside `result` (see `pip.install`).
- **needs_clarification** — Popo needs the operator to act (e.g. approve an
  egress). Read `clarification.question`, relay it to the operator, and **re-submit
  the same request** (a new id) once they say it's approved.

## Liveness — is Popo running?

Popo only services requests while the operator runs `iceclimber serve`. While it
runs, it rewrites `$ROOT/protocol/heartbeat` with content `"<seq> <iso8601>"`,
where `<seq>` increases every cycle.

Judge liveness on **`<seq>` advancing across your polls**, *not* on the timestamp
(your clock and Popo's may differ). Poll with backoff: ~2s, 5s, 10s, then 30s.
If `<seq>` has not advanced across the last several polls (~2 minutes), stop
waiting and tell the operator: *"Popo appears to be down — please run `iceclimber
serve`."*

## First action: report your capabilities

As your very first action, write `$ROOT/protocol/capabilities.json`:

```json
{ "has_exec": true, "has_file_write": true }
```

`has_file_write` must be true (you're using it). Set `has_exec` to whether your
harness can **execute a binary at an absolute path** — Popo can *install* Python
either way, but you can only *run* it if `has_exec` is true. This is informational;
Popo never depends on it.

## The verbs

### `ping` — check the bridge
```jsonc
params: {}
result: { "pong_at": "...", "popo_version": "0.1.0" }
```

### `python.install` — install a Python interpreter
```jsonc
params: { "version": "3.12" }     // minor version; Popo pins the exact patch
result: { "version": "3.12.13",
          "path": "/abs/$ROOT/runtimes/python/3.12.13-aarch64-musl/bin/python3",
          "already_installed": false }
```
Run it by the absolute `path`, e.g. execute `<path> -c "print(1)"`.

### `pip.install` — install packages into an installed interpreter
```jsonc
params: {
  "python_version": "3.12",
  "packages": [ { "name": "requests", "version": "2.32.3" },  // pinned
                { "name": "rich" } ]                           // or unversioned
}
result: {
  "installed": [ { "name": "requests", "version": "2.32.3", "tier": "mirror", "sha256": "..." } ],
  "failed":    [ { "name": "foo", "version": "1.0", "error": "..." } ]
}
```
`status` is `ok` even if some packages are in `failed`. Unversioned packages
resolve to whatever the package manager picks; the exact version is recorded in
`installed`.

### `node.install` — install a Node.js runtime
```jsonc
params: { "version": "20" }       // version line; Popo pins the exact release
result: { "version": "20.11.1",
          "path": "/abs/$ROOT/runtimes/node/20.11.1-aarch64-musl/bin/node",
          "already_installed": false }
```
Run it by the absolute `path`; npm sits beside it at `bin/npm`. (On musl sandboxes,
arm64 needs Node ≥ 24.)

### `npm.install` — install npm packages into an installed Node runtime
```jsonc
params: {
  "node_version": "20",
  "packages": [ { "name": "left-pad", "version": "1.3.0" },   // pinned
                { "name": "chalk" } ]                          // or unversioned
}
result: {
  "installed": [ { "name": "left-pad", "version": "1.3.0", "tier": "relay" } ],
  "failed":    [ ],
  "node_path": "/abs/$ROOT/runtimes/node/20.11.1-aarch64-musl/lib/node_modules"
}
```
To use the packages, run node with `NODE_PATH` set to `node_path`, e.g.
`NODE_PATH=<node_path> <node> -e "console.log(require('left-pad'))"`. CLI tools
land beside `bin/node`. Pure-JS packages only for now (no native/binary addons).

### `web.fetch` — fetch a URL
```jsonc
params: { "url": "https://...", "method": "GET", "headers": {}, "body": null }
result: {
  "status_code": 200,
  "venue": "sandbox-exec",        // or "controller"
  "encoding": "utf8",             // or "base64" for non-text inline bodies
  "body_inline": "...",           //  small bodies, inline
  "body_blob": "blobs/<sha256>"   //  large bodies — read $ROOT/protocol/blobs/<sha256>
}
```
A fetch that must go out through Popo's own network may come back
`needs_clarification` (the operator has to approve the destination). Relay the
question, then re-submit after they approve.

## Notes

- One request, one response, matched by `id`.
- You never need to read or write anything in `inbox/cur` or `outbox/cur` — Popo
  manages those.
- If something is unclear or a response is taking too long, check the heartbeat
  before assuming failure.
