# Node application scenario

A self-contained, full-stack end-to-end test: it provisions a Node runtime and npm
packages in the sandbox, fetches data through Popo, builds and runs a real Node
program, and asserts its computed output — the scripted equivalent of the agent
demo, for Node.

## What it exercises

- **`web.fetch`** — pulls `https://xkcd.com/info.0.json` through Popo (the sandbox
  venue; `xkcd.com` is configured `reachable_from: sandbox`, so it's ungated).
- **`node.install`** — a portable Node 24 runtime (arm64-musl needs Node ≥ 24).
- **`npm.install` (relay)** — `figlet` + `cli-table3`, downloaded by Popo's npm and
  relayed into the runtime.
- **Execution** — runs `app/index.js` with `NODE_PATH`, which reads the comic,
  computes statistics, and renders a figlet ASCII banner + a cli-table3 table.

The assertions check the rendered report carries the comic's **number**, **title**,
and computed **title length** — recomputed from the same fetched JSON, so it's
robust to whichever comic is current.

## Files

- `app/index.js` — the application (embedded into the test via `go:embed`).
- `node_app_test.go` — the scenario (uses `test/scenarios/harness` for the sandbox
  plumbing).

## Running

```sh
make sandbox-up   # the Lima/Alpine VM (shared with the functional suite)
make scenario     # build iceclimber + run every scenario
# or just this one:
go test -tags scenario -count=1 -run TestNodeApp ./test/scenarios/node/...
```

Requires Lima and — for the npm relay — `npm` on the controller (this host); the
scenario skips if either is missing.
