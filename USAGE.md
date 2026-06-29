# Using iceclimber to build applications

This is the practical, start-to-finish guide: take a capable agent dropped into a
locked-down sandbox — no Python, no Node, no Java, no package installs, maybe no
internet — and let it **build and run real applications** there. iceclimber's
controller (**Popo**) provisions everything the agent asks for over an SSH link,
with **you approving each step**. The agent (**Nana**) just reads and writes files.

> New to the design? The [README](README.md) has the architecture; this guide is how
> you actually *use* it. Air-gapped sandbox? Also read [DEMO.md](DEMO.md).

## What you need

- **A sandbox** — any host you can reach over SSH (a corp VM, a container, a CI
  runner). The agent runs *inside* it. The only hard requirement: it can **read/write
  files and exec** what gets installed. No daemon, no open ports, no pre-installed
  toolchains.
- **The controller (Popo)** — your laptop, with the `iceclimber` binary and network
  access (Popo fetches runtimes/packages on the agent's behalf). For **Tier-1 relay**
  into an air-gapped sandbox, your laptop also needs the matching toolchain it relays
  with: `python3` (pip), `npm` (npm), `java` (Maven). Tier-0 (sandbox reaches a mirror)
  needs none of these.
- **An agent in the sandbox** — anything that can read/write files and run commands.
  Claude Code is the worked example.

## 1. Point Popo at the sandbox

```sh
make build                       # → ./iceclimber
./iceclimber init                # scaffolds iceclimber.yaml in the current dir
# edit iceclimber.yaml: ssh.host / ssh.user / ssh.port / ssh.identity_file, sandbox_id
./iceclimber trust               # record the sandbox's SSH host key (see below)
./iceclimber probe               # read-only: connectivity + sandbox fingerprint (os/arch/libc, install root)
```

`trust` records the box's SSH host key in `known_hosts` so iceclimber will connect —
the in-CLI replacement for an out-of-band `ssh-keyscan`, which is the awkward step
for ephemeral sandboxes. It prints the key's SHA256 fingerprint and asks you to
confirm; for automation pass `--fingerprint SHA256:…` (record only on a match) or
`--yes`, and `--replace` when a rebuilt box reuses an address with a new key. The
bare-`iceclimber` console also offers this as a modal on first connect. (Keys are
never trusted silently — that's the security floor.)

`probe` changes nothing — it just confirms Popo can reach the box and reports where
it would install. Fix any connection issues here before going further.

## 2. Bootstrap the sandbox

```sh
./iceclimber bootstrap           # creates the protocol tree, drops skill/NANA.md, ping/pong smoke test
```

This creates `<root>/protocol/` (the maildir the agent and Popo exchange files
through), `<root>/runtimes/`, and writes the agent's instruction sheet to
`<root>/skill/NANA.md`. The smoke test proves the round-trip works end to end.

## 3. Wire your agent to NANA.md  ← the crucial step

The agent learns to use Popo from **one short file**: `<root>/skill/NANA.md`. It tells
the agent to talk to Popo with the **`popo` client** (`bootstrap` relays `popo` into
`<root>/popo`): `popo help` lists the actions (install runtimes/packages, fetch web
data) and `popo <verb> …` does the rest — the agent never hand-builds the protocol.
**Add NANA.md to your agent's instructions so it reads it first** — otherwise the
agent has no idea Popo exists and will just hit "no internet, no Python" walls. (A
harness that can't execute `popo` falls back to the raw file protocol in
`<root>/skill/PROTOCOL.md` — same bridge, file I/O only.)

For a Claude Code agent in the sandbox, put a line like this in its system prompt or
`CLAUDE.md`:

> Before anything else, read `<root>/skill/NANA.md` and follow it. You are in a
> locked-down sandbox; that file is how you install runtimes and packages and fetch
> web data through the controller (Popo).

(`./iceclimber skill print` shows the exact contract; `skill path` prints where it
lives in the sandbox.)

**Don't have an agent in the sandbox yet?** iceclimber can install one for you:

```sh
export CLAUDE_CODE_OAUTH_TOKEN=...        # subscription token (claude setup-token)
./iceclimber agent install claude         # relays the Claude Code binary in + configures auth
```

The controller downloads the Claude Code binary **for the sandbox's platform** and
relays it in (no sandbox network needed — it works on a fully air-gapped box), then
writes its **subscription** token to a 0600 env file (API key refused — never
metered billing) and drops a **`nana` launcher** at the tree root.
`./iceclimber agent list` shows the available agents.

**Starting the agent — `$ROOT/nana`.** Run it *inside the sandbox* from any
directory; it sets up the environment (auth token, `ANTHROPIC_API_KEY` blanked) and
launches the agent with `NANA.md` already loaded as its system context, so it knows
how to reach Popo from turn one. Extra flags pass straight through to the agent:

```sh
$HOME/.iceclimber/nana                     # interactive session, wired to NANA.md
$HOME/.iceclimber/nana -p "build me a ..."  # headless; flags after the name go to the agent
```

(With more than one agent installed, name it: `nana claude [-- args]`. With one, it's
the default.)

## 4. Run the console and let the agent work

```sh
./iceclimber                     # the operator console: serves the sandbox + approve inline
```

Bare `iceclimber` opens a split-pane cockpit — `[POPO]` is what the controller does,
`[NANA]` is the sandbox's own voice. **You don't pass `--agent-log`** — when you run
the agent headless via `nana` (e.g. `nana -p "…"`), it mirrors its stream to a sandbox
session log, the serving Popo bridges that to a controller-side `agent.log`, and the
console tails it — so the agent's narration shows up in `[NANA]` on its own. The same
holds for **`iceclimber tui`** and **`iceclimber logs -f`** (also no flag — they
default to the same bridged log), so whichever view you prefer shows Nana. (Interactive
`nana` sessions run on their own terminal and aren't mirrored; `--agent-log <file>`
remains an explicit override.) To see the agent's **tool calls** in `[NANA]` (not just
its final summary), run it with `--output-format stream-json --verbose`
(`nana -p "…" --output-format stream-json --verbose`) — the bridge renders those
events into readable `→ Bash: …` lines. As the agent asks Popo for things, you'll get
an inline approval modal for each:

```
  ╭─────────────────────────────────────────────
  │ Approve operation · sandbox my-box
  │ Install Python packages
  │   python    3.12
  │   packages  rich
  ╰─────────────────────────────────────────────
    [y] approve   [a] approve all pip.install   [n] deny   [d] deny+remember
```

Approve with `y` (or `a` to approve all of that kind for the session). Each fetch
that leaves *your* network is gated separately and flagged. The agent then builds and
runs its program using the **absolute paths** Popo returned.

You can also drive the sandbox yourself from the console: **`i`** install a
runtime + packages, **`b`** re-provision, **`s`** live status, **`e`** manage egress
rules, **`q`** quit.

**Prefer scripting/CI?** Everything works headless too: `iceclimber serve` (add
`--yes` to auto-approve unattended), `iceclimber install python 3.12`,
`iceclimber install pip rich --python 3.12`, `iceclimber logs -f`, etc.

## What the agent can build

Through Popo, the agent can provision and use three stacks plus web data, so it can
build whatever they support — data-processing scripts, CLIs, small services, demos:

| Stack | Runtime | Packages |
|---|---|---|
| **Python** | `python.install` (python-build-standalone) | `pip.install` (any PyPI package) |
| **JavaScript** | `node.install` (Node + npm) | `npm.install` → a `NODE_PATH` to `require()` |
| **Java** | `java.install` (Temurin JDK + javac) | `maven.install` → a `classpath` for `javac`/`java` |

Plus **`web.fetch`** for data (gated — through the sandbox's own network if it has
one, or relayed through Popo's network when it doesn't).

**Worked examples** that fetch data, provision a stack, and build + run a real
program live in [`test/scenarios/`](test/scenarios/) — one per language
([`python/`](test/scenarios/python/), [`node/`](test/scenarios/node/),
[`java/`](test/scenarios/java/)). They're the clearest "here's a real app, end to
end" reference.

## Getting great results

- **Point the agent at NANA.md first** (step 3) — it's the difference between a stuck
  agent and a productive one.
- **Use absolute paths.** Installed runtimes are run by their absolute path, not via
  `PATH` — NANA.md spells this out and Popo returns the path in every install result.
- **Batch package installs** into one request (e.g. all your pip packages at once).
- **Air-gapped sandbox?** Approve egress hosts in the console as they come up, or
  pre-approve them for an unattended run. The full air-gapped walkthrough — a real
  agent building apps with zero sandbox internet — is in [DEMO.md](DEMO.md).
- **Watch what's happening** in the console's `[POPO]`/`[NANA]` panes, or with
  `iceclimber logs -f --agent-log <file>`.

## Where things live in the sandbox

```
<root>/
  protocol/      # the maildir: outbox (agent → Popo), inbox (Popo → agent), heartbeat
  runtimes/      # installed Python / Node / Java, run by absolute path
  skill/NANA.md  # the agent's instruction sheet
  work/          # a good place for the agent to build (your convention)
```

That's it — point Popo at a box, bootstrap, wire your agent to NANA.md, run the
console, and approve as your agent builds.
