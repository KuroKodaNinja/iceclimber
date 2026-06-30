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
popo python.install 3.12               # install a runtime
popo pip.install --python 3.12 rich    # install packages into it
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

## If you cannot run programs

If your harness can only read and write files (no execute), you can't run `popo`.
Talk to Popo with the **raw file protocol** instead — it needs only file I/O. See
`$ICECLIMBER_HOME/skill/PROTOCOL.md`.
