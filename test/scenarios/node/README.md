# Node application scenario

A self-contained, full-stack end-to-end test: it provisions a Node runtime and npm
packages in the sandbox, fetches data through Popo, builds and runs a real Node
program, and asserts its computed output — the scripted equivalent of the agent
demo, for Node.

## What it exercises

It builds a **real npm project** (a `package.json` project, not loose packages):

- **`web.fetch`** — pulls `https://xkcd.com/info.0.json` through Popo (the sandbox
  venue; `xkcd.com` is configured `reachable_from: sandbox`, so it's ungated).
- **`node.install`** — a portable Node 24 runtime (arm64-musl needs Node ≥ 24).
- **`npm.install --project` (manifest-driven relay)** — the project's `package.json`
  (`blessed` + `blessed-contrib`) is installed by Popo's npm and the whole
  `node_modules` tree is relayed beside it. `blessed-contrib`'s `node_modules` carries
  `.bin` **symlinks**, so this also exercises the idempotent symlink relay; `blessed` is
  the required peer, and the app runs with ordinary local `./node_modules` resolution
  (no `NODE_PATH`).
- **Execution** — runs `app/index.js` from the project dir: it reads the comic and
  renders a headless **blessed-contrib** terminal dashboard (a grid + bar chart), then
  prints the computed values.

The assertions check the app printed `DASHBOARD_OK` (both libraries loaded and drove
widgets) plus the comic's **number**, **title**, and computed **title length** —
recomputed from the same fetched JSON, so it's robust to whichever comic is current.

## Files

- `app/package.json` — the project manifest (embedded via `go:embed`).
- `app/index.js` — the application (embedded via `go:embed`).
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
