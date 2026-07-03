# QUICKSTART — an air-gapped sandbox, from zero

You have: a locked-down Linux box you can SSH into (no Python, no packages, no
outbound network) and an agent you want working inside it. This page is the
shortest path. (Concepts + every option: [README](README.md); the full guide:
[USAGE](USAGE.md).)

## 0. What you need

- The `iceclimber` binary on **your** machine (the controller — it has network).
  Build with `make build` or grab a release.
- SSH access to the sandbox (key, agent, or password; `~/.ssh/config` aliases and
  ProxyJump/bastions work).
- Nothing on the sandbox. Really — BusyBox `sh` and SFTP-or-exec is enough.

## 1. One command

```sh
iceclimber setup
```

It walks you through everything, each step skippable and idempotent:

1. **config** — asks for the SSH host/user, writes `iceclimber.yaml`
2. **trust** — shows the sandbox's host-key fingerprint, records it on your OK
3. **bootstrap** — fingerprints the box (arch/libc), drops the protocol tree,
   the `popo` client, and the agent contract (`NANA.md`)
4. **shell** *(optional)* — a static bash so `iceclimber shell` has history +
   tab-completion
5. **agent** *(optional)* — relays Claude Code in, wires its subscription token
   (from `claude setup-token`) and NANA.md
6. drops you into the **console**

Scriptable/CI form (no prompts):

```sh
iceclimber setup --host 10.0.0.5 --user agent --yes --skip-agent --no-console
```

## 2. Let the agent work

In the sandbox (via `iceclimber shell`, or your own session):

```sh
nana -p "Build a CLI that renders today's xkcd as ASCII art"
```

`nana` starts the agent with the sandbox contract wired in. The agent asks Popo
(your console) for runtimes, packages, and web data — **you approve each class of
operation once**, live in the console. Watch both sides stream: `[POPO]` is your
controller acting, `[NANA]` is the agent's own narration.

## 3. The three commands you'll use daily

```sh
iceclimber            # the console: serve + watch + approve (one sandbox pane today)
iceclimber shell      # drop into the sandbox with PATH/env set up
iceclimber logs -f    # the merged [POPO]/[EGRESS]/[NANA] feed, headless
```

## Strict air-gap? (no live egress at all)

The default egress mode is a **gated proxy**: the sandbox's own pip/npm/git work,
tunneled through your machine, every new host held for your approval. If your
policy forbids even that, pin the relay-only mode in `iceclimber.yaml`:

```yaml
egress_mode: relay
```

Now nothing inside the sandbox ever touches a network: Popo downloads
sha256-pinned artifacts on *your* side and pushes files in. The agent's contract
(NANA.md) is rendered to match whichever mode you chose.

## When something's off

Every error names its fixing command, but the usual suspects:

| Symptom | Fix |
|---|---|
| "no config …" | `iceclimber setup` (or `init` + edit) |
| "host key not trusted" | `iceclimber trust` (fingerprint shown; `--replace` after a rebuild) |
| "not bootstrapped" | `iceclimber bootstrap` |
| "already being served by pid …" | another console/serve owns this sandbox — one at a time |
| agent says Popo is down | start `iceclimber` (or `serve`) — requests queue durably meanwhile |
