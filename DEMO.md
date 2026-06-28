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

  Or stash it once so the `make demo*` targets pick it up automatically (the
  `.demo/` directory is gitignored):

  ```sh
  mkdir -p .demo
  echo 'export CLAUDE_CODE_OAUTH_TOKEN=<token>' > .demo/token.env
  ```

  > A live agent run consumes your subscription usage. The harness always launches
  > the agent with `ANTHROPIC_API_KEY` emptied, so it can't silently bill the API.

---

## Live walkthrough

Boot the VM once (the first boot installs node + Claude Code, so it's slow):

```sh
make demo-up
```

The live demo uses **two terminals** (a third is optional). `serve` runs
**supervised** in the foreground and pauses for you to approve each operation
inline — Claude-Code style.

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

The agent reads `NANA.md` and asks Popo for what it needs. Back in **Terminal A**
you'll be prompted to approve each step, with context:

```
  ╭─────────────────────────────────────────────
  │ Approve operation · sandbox iceclimber-demo
  │ Install Python packages
  │   python    3.12
  │   packages  rich, pyfiglet
  ╰─────────────────────────────────────────────
    [y] approve   [a] approve all pip.install   [n] deny   [d] deny+remember   [?]
```

- `Install Python …` → `y`
- `Install Python packages · rich, pyfiglet` → `y`
- `web.fetch GET https://xkcd.com/info.0.json` (⚠ *leaves YOUR network*) → `y`

> Each prompt *is the gate working*: nothing installs, and no byte leaves your
> machine's network, without your say-so. Approving a fetch returns the **real
> result in the same pass** — no re-submit. `[a]` approves all of that type for the
> session; `[n]`/`[d]` deny (the agent gets `operator_denied`).

When the agent finishes in Terminal B (it prints the comic report — an ASCII
banner, a computed stats table, and a bar chart), press **Ctrl-C** in Terminal A.
`make demo-live` then verifies and prints **`DEMO VERIFY: PASS`** — proving the
program used `rich` + `pyfiglet`, computed stats (e.g. the title's character
count) from the fetched data, and rendered it, all bridged through Popo.

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
