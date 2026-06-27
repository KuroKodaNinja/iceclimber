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

  > A live agent run consumes your subscription usage. The harness always launches
  > the agent with `ANTHROPIC_API_KEY` emptied, so it can't silently bill the API.

---

## Live walkthrough

### 1. Boot + provision the demo VM

```sh
make demo-up          # Alpine + node + Claude Code + musl deps (first boot is slow)
make demo-bootstrap   # build iceclimber, point a config at the VM, create the tree + NANA.md
```

`demo-bootstrap` prints the tree **root** (e.g. `/home/<you>.guest/iceclimber-demo`)
and writes `iceclimber-demo.yaml` (gitignored) for the host side.

### 2. Air-gap the sandbox

```sh
make demo-firewall    # egress now: DNS + 443 to Anthropic only — nothing else
```

Prove it really is sealed (these should **fail**, while the agent's API does not):

```sh
make demo-shell
  pip install rich            # -> network failure (no PyPI)
  curl https://xkcd.com       # -> hangs/timeout (no general web)
  exit
```

### 3. Terminal A — Popo (host)

```sh
./iceclimber serve --config iceclimber-demo.yaml
```

Leave it running. This is the only thing the sandbox can reach besides its own API.

### 4. Terminal B — launch the agent, and watch the gate (host)

```sh
make demo-agent       # runs Claude *inside* the VM, on the task in test/demo/TASK.md
```

Watch both terminals. The agent reads `NANA.md`, then drives the maildir to
install Python 3.12 and `rich` — each serviced by Popo in Terminal A, because the
agent can reach neither directly. Then it tries to **fetch
`https://xkcd.com/info.0.json`**, which goes out through *Popo's* network (the
controller venue) and is **gated**. The fetch is held (`needs_clarification`), and
because this is a one-shot headless run, the agent prints the exact
`iceclimber approve …` command and **stops**.

> That stop *is the gate working*: the agent cannot reach the network — not even
> its requested URL — without your explicit approval.

### 5. Approve, then complete the run

From **Terminal A**, approve the held fetch:

```sh
./iceclimber pending --config iceclimber-demo.yaml          # shows the held fetch + its id
./iceclimber approve <id> --config iceclimber-demo.yaml     # persists a host allow rule
```

Then re-run the agent. Approvals persist, so this pass sails through the fetch and
finishes the job:

```sh
make demo-reset       # clear the maildir for a clean pass (keeps runtimes + the approval)
make demo-agent       # now allowed: the agent fetches, writes work/comics.py, and runs it
```

> Why `demo-reset`? A held request's id already has a response, and Popo's
> effectively-once dedup won't re-service the same id. Clearing the maildir lets
> the fresh pass start clean. (A continuously-running *interactive* agent would
> instead re-submit under a **new id**, as `NANA.md` instructs.)

### 6. Verify

```sh
make demo-verify      # runs the agent's program; checks it renders the fetched comic
```

A `PASS` line means the program the agent built prints the comic number and title
it obtained through Popo — Python, `rich`, and the data all bridged in.

> **Prefer a hands-off run?** Pre-approve the host *before* launching, so the
> single pass completes without stopping at the gate:
> ```sh
> ./iceclimber web fetch https://xkcd.com/info.0.json --config iceclimber-demo.yaml   # hold once
> ./iceclimber approve "$(./iceclimber pending --config iceclimber-demo.yaml | awk 'NR==1{print $1}')" --config iceclimber-demo.yaml
> make demo-reset && make demo-agent && make demo-verify
> ```
> This is exactly what the automated `make demo` does (see below).

### 7. Teardown

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
