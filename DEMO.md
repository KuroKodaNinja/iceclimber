# Acceptance demo — a real Claude agent in an air-gapped sandbox

Every functional test runs against a VM with **open internet**, so they prove the
*mechanism* but not the **premise**. This demo proves the premise: a sandbox that
genuinely *can't* reach the network, with a **real Claude agent living inside it**,
uses iceclimber to get the Python, package, and web data it needs — and builds and
runs a working program.

The sandbox is air-gapped down to **only the Claude API** (so the agent can think)
— PyPI, GitHub, and the data URL are all unreachable. The only way out is Popo.

Two ways to run it:

- **`make demo-live`** path below — you watch the agent work, and approve its
  egress request with your own eyes.
- **`make demo`** — the same thing, fully automated, asserting the result. The CI
  acceptance gate. See [the automated run](#automated-run).

---

## Prerequisites

- Lima (`limactl`) with the `vz` backend, as for the functional sandbox.
- A **Claude subscription** (Pro/Max). The agent authenticates with a
  subscription OAuth token — **never the metered API**. Mint one once on the host:

  ```sh
  claude setup-token        # browser handshake; prints a token
  export CLAUDE_CODE_OAUTH_TOKEN=<the token it prints>
  ```

  Or stash it once so the `make demo*` targets pick it up automatically (the file
  is gitignored):

  ```sh
  echo 'export CLAUDE_CODE_OAUTH_TOKEN=<token>' > .demo-token.env
  ```

  > A live agent run consumes your subscription usage. The harness always launches
  > the agent with `ANTHROPIC_API_KEY` emptied, so it can't silently bill the API.

---

## Live walkthrough

Boot the VM once (the first boot installs node + Claude Code, so it's slow):

```sh
make demo-up
```

Then the whole guided demo is a single command:

```sh
export CLAUDE_CODE_OAUTH_TOKEN=...    # subscription token (see Prerequisites)
make demo-live
```

`make demo-live` points a config at the VM, creates the tree + `NANA.md`,
**air-gaps** the sandbox, starts Popo's `serve` in the background, and runs the
agent in two passes:

1. **Pass 1 (you watch).** The agent reads `NANA.md` and drives the maildir to
   install Python 3.12 and `rich` — each serviced by Popo, because the agent can
   reach neither directly. Then it tries to **fetch `https://xkcd.com/info.0.json`**,
   which goes out through *Popo's* network (the controller venue) and is **gated**.
   The fetch is held, so the agent prints an `iceclimber approve …` command and
   stops.
   > That stop *is the gate working*: the agent cannot reach the network — not even
   > its requested URL — without your explicit approval. (A one-shot headless
   > `claude -p` can't block waiting for an out-of-band approval, hence two passes.)

2. **You approve**, in another terminal:
   ```sh
   ./iceclimber pending --config iceclimber-demo.yaml          # the held fetch + its id
   ./iceclimber approve <id> --config iceclimber-demo.yaml     # persists a host allow rule
   ```
   then press **Enter** back in the `make demo-live` terminal.

3. **Pass 2.** The fetch is now allowed; the agent fetches, writes
   `work/comics.py`, runs it, and `demo-live` verifies. A **`DEMO VERIFY: PASS`**
   line means Python, `rich`, and the data all bridged in through Popo.

On exit it restores egress and stops `serve`.

### Watch it happen (merged log)

In a **third terminal**, tail Popo's activity and the agent's stream merged into
one feed:

```sh
make demo-logs
```

`[POPO]` lines are what the controller services (`python.install → ok`,
`pip.install → ok rich …`, `web.fetch → held`, then `approved`, then
`web.fetch → ok`); `[NANA]` lines are the agent's own actions. The same view
works for any run — `iceclimber logs -f --config <cfg> [--agent-log <file>]`; the
structured source is `~/.iceclimber/<sandbox_id>/activity.jsonl`. `serve` also
prints this feed on its own stdout.

### Prove the air-gap is real

While the VM is air-gapped, these **fail** — so everything the agent achieves, it
achieves *only* through Popo:

```sh
make demo-shell
  pip install rich        # -> network failure (no PyPI)
  curl https://xkcd.com   # -> hangs/timeout (no general web)
  exit
```

### Drive it by hand instead

`make demo-live` is a convenience over the individual targets. To step through it:

```sh
make demo-bootstrap                                   # config + tree + NANA.md
make demo-firewall                                    # air-gap
./iceclimber serve --config iceclimber-demo.yaml      # Terminal A (Popo)
make demo-agent                                       # Terminal B — provisions, holds at the gate
./iceclimber approve <id> --config iceclimber-demo.yaml
make demo-reset && make demo-agent                    # clean pass; completes
make demo-verify
```

> `demo-reset` is needed because Popo's effectively-once dedup won't re-service a
> *held* id; a re-submit under a **new id** (as `NANA.md` instructs) would also work.

### Teardown

```sh
make demo-firewall-down   # restore egress (optional)
make demo-down            # stop + delete the VM
```

---

## What to notice

- **The air-gap is real** — step 2's `pip install` / `curl` failures are the proof.
  Everything the agent accomplishes, it accomplishes *only* via Popo.
- **The agent never sees SSH or the maildir internals** — it follows `NANA.md`'s
  abstract file read/write/exec actions. Any agent that can read and write files
  could be Nana; Claude is just the one we dropped in.
- **Egress stays gated even for the agent** — the controller venue is a deliberate
  approval checkpoint, not an open tunnel. You are the one who lets the comic out.
- **Subscription, not API** — `make demo-agent` refuses to run without
  `CLAUDE_CODE_OAUTH_TOKEN`, and empties `ANTHROPIC_API_KEY`.

---

## Automated run

```sh
export CLAUDE_CODE_OAUTH_TOKEN=...   # subscription token, as above
make demo                            # demo-up + the //go:build demo acceptance test
```

`make demo` does the whole sequence headless — boot, bootstrap, **pre-approve**
the fetch host (no human in the loop), air-gap, serve, run the agent, and assert
the program's output — then exits non-zero on any failure. It's opt-in (the `demo`
build tag, never part of `make test`) and needs a host with Lima/`vz` and the
`CLAUDE_CODE_OAUTH_TOKEN` secret. This is the gate to run in CI.
