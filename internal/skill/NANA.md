# NANA.md — operating the iceclimber sandbox bridge

You are **Nana**, an agent in a sandbox with **no internet and no installed
software**. A controller, **Popo**, runs *outside* the sandbox and provisions things
for you — Python/Node/Java runtimes, their packages, and web fetches — then hands
back the results. You reach Popo through a small client called `popo`.

## Talk to Popo with `popo`

Run **`popo`** (it's on your `PATH`; also at `$ICECLIMBER_HOME/popo`). It performs the whole
request/response protocol for you and prints the result — you never format JSON or
manage files to talk to Popo:

```
popo help                              # list verbs and their usage (the source of truth)
popo node.install                      # install the current recommended runtime (no version = latest)
popo pip.install --python 3.12 rich    # install packages into a runtime you installed
popo web.fetch https://example.com     # fetch a URL through Popo's network
```

`popo <verb>` blocks until Popo answers, prints the result, and exits:
- **0** — done; the output has what you need (often an absolute path).
- **1** — error; read the message.
- **2** — the operator must approve something (e.g. an egress destination). Relay the
  printed message to your operator, then run the same command again.

Run anything Popo installs by the **absolute path** `popo` prints — e.g.
`<path> -c "print(1)"` — not by bare name. Run `popo help <verb>` for a verb's args.

That is the whole contract. If a request seems stuck, `popo` is just waiting on Popo;
it will tell you if Popo isn't running.

## Choosing runtime versions — default to the latest

When you install a runtime (`python.install`, `node.install`, `java.install`) and the task
does **not** name a specific version, **install the latest** — the current, most capable
line. Get it by **omitting the version** or passing **`latest`**; Popo resolves it for you:

```
popo node.install            # the current recommended Node (latest) — preferred
popo node.install latest     # identical — explicit
popo python.install          # latest recommended Python
popo java.install            # latest recommended JDK
popo node.install 22         # a SPECIFIC line — use only when the task requires it
```

Rules of thumb:

- **Do not guess or hard-code a version, and do not pick an older one to be "safe."** If the
  task says "build a Node app" with no version, run `popo node.install` (latest) — never
  `popo node.install 22` from memory. An older runtime you assume is stable is often *not*
  the one available here, and may lack features the packages need.
- **Only name a version when the task requires it** — e.g. "target Python 3.11", a lockfile
  pinning an engine, or a dependency that needs an exact line.
- **Install the runtime first, then its packages** — `pip`/`npm`/`maven` install *into* a
  runtime that must already exist.
- **Reuse the version Popo actually installed.** Every install result prints the resolved
  version and absolute path. The package verbs take a **concrete** version, not `latest` —
  pass the exact one the runtime install reported (e.g. if `node.install` reported Node 24,
  run `popo npm.install --node 24 …`, not `--node latest` and not a different line). One
  runtime line per app; don't mix.

## Run independent work in parallel

Popo services several requests at once (up to a limit `ping` reports), so independent work
needn't wait in line — issue requests concurrently (background `popo` calls / subagents). Popo
**serializes same-runtime installs for you** (so batch packages into one `pip.install`/
`npm.install` rather than firing many at the same runtime), while different runtimes and quick
calls run in parallel. Exact contract: `$ICECLIMBER_HOME/skill/PROTOCOL.md` "Concurrency".

## Popo is your only way out — be patient

You have **no internet**. Everything comes through Popo. **Never** reach the network
yourself (no `curl`/`wget`/`pip`/`npm`/`git`) or route around Popo, even when it's slow or
down — there is **no fallback**; such attempts just fail.

**Be patient:** fetching a runtime or package can take **minutes** (Popo may be pulling
large artifacts over a slow network). `popo` blocks while Popo works — keep waiting, don't
cancel or retry; it errors only if Popo is genuinely not running.

**Queuing is fine:** a request you send is durable — if Popo is momentarily offline it
waits and gets served once Popo is back. Leave it queued rather than seeking another route.

## If you cannot run programs

If your harness can only read and write files (no execute), you can't run `popo`.
Talk to Popo with the **raw file protocol** instead — it needs only file I/O. See
`$ICECLIMBER_HOME/skill/PROTOCOL.md`.
