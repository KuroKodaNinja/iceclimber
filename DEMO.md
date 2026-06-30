# Acceptance demo — a real Claude agent in an air-gapped sandbox

Every functional test runs against a VM with **open internet**, so they prove the
*mechanism* but not the **premise**. This demo proves the premise: a sandbox that
genuinely *can't* reach the network, with a **real Claude agent living inside it**,
uses iceclimber to get the runtimes, packages, and web data it needs — and builds
and runs a working program in **three languages** (Python, JavaScript, and Java).

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

  Or stash it once so the `make demo*` targets pick it up automatically (the
  `.demo/` directory is gitignored):

  ```sh
  mkdir -p .demo
  echo 'export CLAUDE_CODE_OAUTH_TOKEN=<token>' > .demo/token.env
  ```

  > A live agent run consumes your subscription usage. `iceclimber agent install
  > claude` writes the token into the sandbox with `ANTHROPIC_API_KEY` emptied, so
  > the agent can't silently bill the API.

The agent itself is installed by the official command —
**`iceclimber agent install claude`** — which downloads the Claude Code binary for
the sandbox's platform on the controller, relays it into the sandbox (no sandbox
network needed — it works air-gapped), and writes its subscription auth to a 0600
env file. The demo runs this for you (inline in `make demo`/`make demo-live`, or as
`make demo-agent-install` in the manual flow).

---

## Live walkthrough

Boot the VM once (provisions the agent's musl prereqs — the Claude CLI itself is
installed later by `iceclimber agent install claude`):

```sh
make demo-up
```

The live demo uses **two terminals** (a third is optional). `serve` runs
**supervised** in the foreground and pauses for you to approve each operation
inline — Claude-Code style.

> **Prefer the graphical console?** After the VM is up and air-gapped (`make
> demo-up` then `make demo-firewall`), run **`make demo-console`** in Terminal A
> instead of `make demo-live`. Bare `iceclimber` serves the sandbox in a live
> split-pane cockpit and surfaces each approval as a modal you answer in-place
> (`y`/`a`/`n`/`d`) — same gating, richer view. Then run `make demo-agent` in
> Terminal B as below. (`make demo-live` stays the scripted/verified path.)

**Terminal A — set up + serve (supervised):**

```sh
export CLAUDE_CODE_OAUTH_TOKEN=...    # subscription token (see Prerequisites)
make demo-live
```

`make demo-live` points a config at the VM, creates the tree + `NANA.md`,
**air-gaps** the sandbox, then runs Popo's `serve` in the foreground. It will pause
with a prompt before each operation the agent requests.

**Terminal B — start the agent:**

```sh
make demo-agent
```

`make demo-agent` starts Claude through the sandbox's **`nana` launcher**
(`$ICECLIMBER_HOME/nana`, written by `agent install`) — the same wrapper a real operator uses:
it sets up auth and loads `NANA.md` as the agent's system context. The agent then
asks Popo for what it needs. Back in **Terminal A** you'll be prompted to approve
each step, with context:

```
  ╭─────────────────────────────────────────────
  │ Approve operation · sandbox iceclimber-demo
  │ Install Python packages
  │   python    3.12
  │   packages  rich
  ╰─────────────────────────────────────────────
    [y] approve   [a] approve all pip.install   [n] deny   [d] deny+remember   [?]
```

The task spans **three languages**, so you'll approve a handful of operations —
`web.fetch` for the xkcd JSON, then `python.install` + `pip.install` (rich),
`node.install` + `npm.install` (left-pad), and `java.install`. Approve each with
`y` (or `[a]` to approve all of a type for the session).

> Each prompt *is the gate working*: nothing installs, and no byte leaves your
> machine's network, without your say-so. Approving a fetch returns the **real
> result in the same pass** — no re-submit. `[a]` approves all of that type for the
> session; `[n]`/`[d]` deny (the agent gets `operator_denied`).

When the agent finishes in Terminal B, it has built and run a small program in
**each** of Python, JavaScript, and Java that reads the one fetched comic and prints
a computed `[<lang>] xkcd #<num> title-length=<N>` line. Press **Ctrl-C** in
Terminal A. `make demo-live` then verifies and prints **`DEMO VERIFY: PASS`** —
proving all three runtimes, the Python/JavaScript packages, and the data were
bridged through Popo, and each program computed the same comic number from it.

**Terminal C (optional) — the merged log, or the graphical dashboard:**

```sh
make demo-logs    # plain merged feed
make demo-tui     # live split-pane [POPO]/[NANA] dashboard
```

`[POPO]` lines are what Popo services plus your approve/deny decisions; `[NANA]`
lines are the agent's own actions. (`serve` already prints the activity feed on its
own stdout in Terminal A.) Both work for any run —
`iceclimber logs -f --config <cfg> [--agent-log <file>]` /
`iceclimber tui --config <cfg> [--agent-log <file>]`; the structured source is
`~/.iceclimber/<sandbox_id>/activity.jsonl`.

### Prove the air-gap is real

While the VM is air-gapped, these **fail** — so everything the agent achieves, it
achieves *only* through Popo:

```sh
make demo-shell
  pip install rich        # -> network failure (no PyPI)
  curl https://xkcd.com   # -> hangs/timeout (no general web)
  exit
```

### Unattended?

`./iceclimber serve --yes` services everything without prompting — that's what the
headless `make demo` does (with egress pre-approved), asserting the result. See
[Automated run](#automated-run).

### Teardown

```sh
make demo-firewall-down   # restore egress (optional)
make demo-down            # stop + delete the VM
```

---

## What to notice

- **The air-gap is real** — step 2's `pip install` / `curl` failures are the proof.
  Everything the agent accomplishes, it accomplishes *only* via Popo.
- **The agent never sees SSH or the maildir internals** — it follows `NANA.md` and
  just runs the `popo` client (`popo <verb> …`). An agent that can't exec could drive
  the same bridge with file read/write alone (PROTOCOL.md); Claude is just the one we
  dropped in.
- **Egress stays gated even for the agent** — the controller venue is a deliberate
  approval checkpoint, not an open tunnel. You are the one who lets the comic out.
- **Subscription, not API** — `iceclimber agent install claude` refuses an API
  key (and refuses to run without `CLAUDE_CODE_OAUTH_TOKEN`), and writes the token
  into the sandbox with `ANTHROPIC_API_KEY` emptied so it can't fall back to metered
  billing.

---

## Automated run

```sh
export CLAUDE_CODE_OAUTH_TOKEN=...   # subscription token, as above
make demo                            # demo-up + the //go:build demo acceptance test
```

`make demo` does the whole sequence headless — boot, bootstrap, **install the
Claude agent** (`iceclimber agent install claude`, while the network is open),
**pre-approve** the fetch host (no human in the loop), air-gap, serve, run the
agent, and assert the program's output — then exits non-zero on any failure. It's opt-in (the `demo`
build tag, never part of `make test`) and needs a host with Lima/`vz` and the
`CLAUDE_CODE_OAUTH_TOKEN` secret. This is the gate to run in CI.
