# Playground — drive iceclimber by hand against the sandbox

A two-terminal walkthrough to watch the protocol work end to end: **Popo**
(controller, your laptop) servicing requests, and **Nana** (inside the sandbox)
submitting them and reading responses. The maildir design only needs file
read/write, so you can *be Nana by hand* — no agent required yet (that's the
`NANA.md` skill doc in a later phase).

## Setup (once)

```sh
make sandbox-up        # boot the Lima/Alpine VM (first run downloads the image)
make build             # build ./iceclimber
make sandbox-config    # write iceclimber.yaml pointing at the VM
./iceclimber bootstrap # create the protocol tree + run a ping/pong smoke test
```

`bootstrap` prints the chosen root, e.g. `/home/<you>.guest/.iceclimber`.

## Terminal A — Popo (host, outside the sandbox)

```sh
./iceclimber serve            # watch loop: heartbeat every 2s, services the outbox
# ./iceclimber serve --transport exec   # force the BusyBox/exec path instead of SFTP
# ./iceclimber serve --once             # one cycle then exit (handy for scripting)
```

Leave it running. Ctrl-C stops it cleanly.

## Terminal B — Nana (inside the sandbox)

```sh
make sandbox-shell                       # = limactl shell iceclimber-sandbox
cd ~/.iceclimber/protocol

watch -n1 cat heartbeat                  # liveness: "1 ...", "2 ...", "3 ..." (Ctrl-C to stop)

# Submit a request by hand: write into tmp/, then mv into new/ (the rename is the
# atomic publish, so Popo never sees a half-written file).
printf '%s' '{"schema_version":1,"id":"hi","type":"ping","created_at":"2026-06-27T00:00:00Z","params":{}}' \
  > outbox/tmp/hi.json
mv outbox/tmp/hi.json outbox/new/hi.json

sleep 2
cat inbox/new/hi.json                    # the pong: {"status":"ok","result":{"pong_at":...,"popo_version":...}}
```

## What to notice

- **Pickup lock** — on service, the request moves `outbox/new` → `outbox/cur`. The
  rename *is* the lock; `ls outbox/cur` shows what's been picked up.
- **Effectively-once** — `mv` the *same filename* into `new/` again and Popo
  dedups: no second response is generated (a response with that id already exists).
- **Liveness** — the heartbeat is `"<seq> <timestamp>"`. Nana judges "is Popo
  alive" on the **counter advancing**, which needs no clock sync between the two
  sides.
- **Crash recovery** — drop a file straight into `outbox/cur` (no response) and run
  `./iceclimber serve --once`: Popo's recovery sweep services the stranded request.

## Where the "real agent" fits

Today you play Nana by hand. To watch an *actual* agent do it, run Claude Code (or
any agent) **inside** the VM and tell it to read/write the same files. The polished
skill document that makes this turnkey — polling schedule, absolute-path contract,
"Popo appears down" handling — is `NANA.md`, a later phase.

## Teardown

```sh
make sandbox-down      # stop and delete the VM
```
