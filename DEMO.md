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

### 4. Terminal B — launch the agent (host)

```sh
make demo-agent       # runs Claude *inside* the VM, on the task in test/demo/TASK.md
```

Watch both terminals. The agent reads `NANA.md`, then drives the maildir to:
install Python 3.12, install `rich`, and **fetch `https://xkcd.com/info.0.json`**
— each one serviced by Popo in Terminal A, because the agent can reach none of
them directly.

### 5. Approve the agent's egress (the interesting moment)

The xkcd fetch goes out through **Popo's** network (the controller venue), which
is **gated**. You'll see the agent's request held (`needs_clarification`).
Approve it from **Terminal A**:

```sh
./iceclimber pending --config iceclimber-demo.yaml          # shows the held fetch + its id
./iceclimber approve <id> --config iceclimber-demo.yaml     # release it
```

The agent re-submits and the fetch returns. (Approve promptly — the agent retries
with backoff.) Use `approve <id> --remember` to persist the allow rule.

### 6. Verify

```sh
make demo-verify      # runs the agent's program; checks it renders the fetched comic
```

A `PASS` line means the program the agent built prints the comic number and title
it obtained through Popo — Python, `rich`, and the data all bridged in.

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
