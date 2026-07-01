# PROTOCOL.md — the raw iceclimber file protocol

Read this only if you **cannot execute programs** in your sandbox (so you can't run
the `popo` client — see `NANA.md`), or if you're implementing/debugging the bridge.
It needs only **read file** and **write file** at absolute paths — no network, no
exec. `popo` does everything below for you; this is the same protocol, by hand.

## The tree

Everything lives under one absolute **install root**, `$ICECLIMBER_HOME` (your operator tells
you its value, e.g. `/home/agent/.iceclimber`). Always use absolute paths.

```
$ICECLIMBER_HOME/
  popo                      # the client (run it if you can; see NANA.md)
  protocol/
    outbox/{tmp,new,cur}/   # your requests to Popo
    inbox/{tmp,new,cur}/    # Popo's responses to you
    heartbeat               # Popo's liveness signal
    capabilities.json       # you write this once (optional, informational)
    blobs/<sha256>          # large response bodies
  runtimes/.../bin/...      # installed interpreters (run by absolute path)
  skill/NANA.md             # the short contract
  skill/PROTOCOL.md         # this file
```

## Make a request

1. **Build the envelope** — a JSON object:

   ```json
   { "schema_version": 1, "id": "<unique id you choose>", "type": "<verb>",
     "created_at": "2026-06-27T00:00:00Z", "params": { ... } }
   ```

   The `id` can be any string unique among your requests; reuse it as the response
   filename.

2. **Deliver it atomically.** Write the JSON to `$ICECLIMBER_HOME/protocol/outbox/tmp/<id>.json`,
   then **rename** it to `$ICECLIMBER_HOME/protocol/outbox/new/<id>.json`. Never write directly
   into `new/` — the rename is what guarantees Popo never reads a half-written file.

3. **Wait for the response.** Poll for `$ICECLIMBER_HOME/protocol/inbox/new/<id>.json` (same id);
   read and parse it when it appears.

4. **Collect it.** After reading the response, mark it collected so Popo can prune the
   request/response pair and the operator's "awaiting collection" count stays honest:
   run `popo collect <id>`, or — if you can't run `popo` — move it yourself by renaming
   `inbox/new/<id>.json` → `inbox/cur/<id>.json`. Best-effort: skipping it only leaves the
   response counted as uncollected (and eventually GC'd by the retention sweep).

## Responses

```json
{ "schema_version": 1, "id": "<same id>",
  "status": "ok",                 // "ok" | "error" | "needs_clarification"
  "completed_at": "...",
  "result": { ... },              // when status == "ok"
  "error": { "code": "...", "message": "...", "retryable": false },
  "clarification": { "question": "..." } }
```

- **ok** — read `result`.
- **error** — read `error.code`/`error.message`. A batch verb can be `ok` overall
  while listing per-item failures inside `result`.
- **needs_clarification** — Popo needs the operator to act (e.g. approve egress).
  Relay `clarification.question`, and **re-submit** the request (new id) once approved.

## Liveness

While the operator runs `iceclimber serve`, Popo rewrites `$ICECLIMBER_HOME/protocol/heartbeat`
with `"<seq> <iso8601>"`, `<seq>` increasing each cycle. Judge liveness on **`<seq>`
advancing** across polls (not the timestamp — clocks differ). Back off ~2s, 5s, 10s,
30s; if `<seq>` hasn't advanced for ~2 minutes, tell the operator to run
`iceclimber serve`.

**Be patient, and never route around Popo.** As long as `<seq>` keeps advancing, Popo is
alive and working — a single request can legitimately take **minutes** (installing a
runtime or downloading/relaying large packages over a slow network is normal). Keep
waiting on the response; do not cancel, re-submit, or try to reach the network yourself
(there is no direct-network fallback — the sandbox has no internet). A request left in
`outbox/new` is **durable**: if Popo is momentarily down, it stays queued and Popo serves
it (and re-services anything mid-flight) once serving resumes — so it is fine to leave
requests queued for Popo to serve later rather than abandoning them.

## Capabilities (informational)

`$ICECLIMBER_HOME/protocol/capabilities.json` is a self-report the **controller**
writes — host facts at bootstrap, the installed agent's identity on `agent
install`/`wrap` — to inform the operator's `status` view. **You don't write it**
(doing so would overwrite the controller's blocks). Popo never depends on it.

## The verbs

| verb | params | result (key fields) |
|---|---|---|
| `ping` | `{}` | `{ pong_at, popo_version }` |
| `python.install` | `{ version }` (minor, e.g. "3.12") | `{ version, path, already_installed }` |
| `pip.install` | `{ python_version, packages:[{name,version?}] }` | `{ installed:[{name,version,tier,sha256}], failed:[{name,version,error}] }` |
| `node.install` | `{ version }` (line, e.g. "24") | `{ version, path, already_installed }` |
| `npm.install` | `{ node_version, packages:[{name,version?}] }` or `{ node_version, project:"<dir>" }` | `{ installed, failed, node_path }` |
| `java.install` | `{ version }` (feature, e.g. "21") | `{ version, path, already_installed }` |
| `maven.install` | `{ java_version, packages:[{name:"group:artifact",version}] }` | `{ installed, failed, classpath }` |
| `maven.build` | `{ project, java_version, goals?:["package"], maven_version? }` | `{ artifacts:[<jar path>], tier:"relay" }` |
| `conda.install` | `{ python_version, packages:[{name,version?}], extra_args?:["-c","conda-forge",…] }` or `{ file:"<environment.yml>" }` | `{ installed:[{name,version,tier,sha256?}], failed }` |
| `web.fetch` | `{ url, method?, headers?, body? }` | `{ status_code, venue, encoding, body_inline? , body_blob? }` |

Notes: run installed runtimes by the absolute `path`/`node_path`/`classpath`
returned. `body_blob` is a `$ICECLIMBER_HOME`-relative path — read the body at `$ICECLIMBER_HOME/<body_blob>`.
For Node, export `NODE_PATH=<node_path>` — or, with `npm.install project:"<dir>"`, the
whole `package.json` is installed and its `node_modules` lands in the project dir (local
resolution, no `NODE_PATH`). `maven.build` builds a sandbox `pom.xml` project with `mvn`
air-gapped (the operator's controller Maven+JDK prime an offline repo; needs both present)
and returns the built jar path(s) under `<project>/target/`. Java 11+ also runs a single
source file directly (`<java> Program.java`). `conda.install` needs the operator to have selected the conda
env_manager for python; it installs into a conda env at `<root>/envs/conda-python-<minor>`
(run its interpreter by that path) and uses conda match-specs (`name=version`, single `=`).
Its `extra_args` allowlist is `-c`/`--channel` (repeatable), `--override-channels`,
`--offline`, `--use-local`. **Tier 0** (default) installs from the sandbox's own channel;
on an **air-gapped box add `--offline`** to `extra_args` to select the **relay** tier — the
operator's controller conda resolves + downloads the packages, pushes a local channel, and
the sandbox installs offline. Results carry `tier` (`mirror` for Tier 0, `relay`) and a
`sha256` on relay-installed packages. One request, one response, matched by `id`. Popo owns
`outbox/cur` (its pickup lock — never write there); you write `outbox/new` and read from
`inbox/new`, then collect into `inbox/cur` (step 4).
