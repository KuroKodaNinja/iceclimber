# Application scenarios

Self-contained, full-stack end-to-end tests that build and run a **real
application** in the sandbox — provisioning the language runtime + packages,
fetching data through Popo, deploying a program, and asserting its output. They are
the scripted equivalent of the agent demo ([`../DEMO.md`](../DEMO.md)), one per
language.

Gated by the `scenario` build tag, so they never run under `make test` or
`make test-functional`.

## Layout

- `harness/` — shared Lima-sandbox plumbing (discover the VM, build the binary, run
  it, push files, dial a RemoteFS). Scenarios import this; it's the only shared
  piece, so each scenario directory stays self-contained.
- `<language>/` — one self-contained scenario per language: its app source, its
  test, and its **own `README.md`** with the operating notes. Currently:
  [`python/`](python/), [`node/`](node/), [`java/`](java/).

## Running

```sh
make sandbox-up   # the shared Lima/Alpine VM
make scenario     # build iceclimber + run every scenario
# or just one language:
go test -tags scenario -count=1 ./test/scenarios/node/...
```

## Adding a scenario

Copy a language directory (e.g. `node/`), swap the app source and the verbs it
exercises, and keep **all** of its operating notes in that directory's `README.md`.
Nothing outside `test/scenarios/` needs to change beyond a one-line mention.
