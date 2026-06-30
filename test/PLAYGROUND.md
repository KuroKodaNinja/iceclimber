# Playground — drive iceclimber by hand against the sandbox

A two-terminal walkthrough to validate the whole v1 experience: **Popo** (the
controller, your laptop) servicing requests, and **Nana** (inside the sandbox)
submitting them and reading responses. The maildir design only needs file
read/write, so you can *be Nana by hand* — exactly what `NANA.md` tells a real
agent to do (`./iceclimber skill print`).

## Setup (once)

```sh
make sandbox-up        # boot the Lima/Alpine VM (first run downloads the image)
make build             # build ./iceclimber
make sandbox-config    # write iceclimber.yaml pointing at the VM
./iceclimber bootstrap # create the tree, run a ping/pong smoke test, drop NANA.md
```

`bootstrap` prints the chosen **root** (e.g. `/home/<you>.guest/.iceclimber`) and
confirms it wrote `skill/NANA.md`. Read the contract a real agent would follow:

```sh
./iceclimber skill print | less
```

## Terminal A — Popo (host, outside the sandbox)

```sh
./iceclimber serve            # watch loop: heartbeat every ~2s, services the outbox
```

Leave it running (Ctrl-C stops it cleanly). Other handy forms:
`serve --once` (one cycle then exit), `serve --transport exec` (force the
BusyBox/exec path), `serve --deny web.fetch` (disable a verb).

## Terminal B — be Nana (inside the sandbox)

```sh
make sandbox-shell                # = limactl shell iceclimber-sandbox
cd ~/.iceclimber/protocol

# Tiny helpers: submit a request, and wait for its response.
req()  { printf '%s' "{\"schema_version\":1,\"id\":\"$1\",\"type\":\"$2\",\"created_at\":\"2026-01-01T00:00:00Z\",\"params\":$3}" \
           > "outbox/tmp/$1.json" && mv "outbox/tmp/$1.json" "outbox/new/$1.json"; }
resp() { for i in 1 2 3 4 5 6 7 8; do [ -f "inbox/new/$1.json" ] && { cat "inbox/new/$1.json"; echo; return; }; sleep 1; done; echo "(no response yet)"; }
```

### 1. First action: ping

(capabilities.json is written by the controller — host facts at bootstrap, the agent's
identity on `agent install`/`wrap` — not by Nana; see PROTOCOL.md.)

```sh
req ping1 ping '{}'
resp ping1            # {"status":"ok","result":{"pong_at":...,"popo_version":...}}

watch -n1 cat heartbeat   # liveness: "1 ...", "2 ...", "3 ..."  (Ctrl-C to stop)
```

### 2. Install Python, then run it (absolute-path contract)

```sh
req py1 python.install '{"version":"3.12"}'
resp py1             # result.path is the absolute bin/python3 to use
# Run it by that absolute path (no PATH/.bashrc reliance):
PY=$(sed -n 's/.*"path":"\([^"]*\)".*/\1/p' inbox/new/py1.json)
"$PY" -c 'import sys; print(sys.version)'
```

### 3. Install a package, then import it

```sh
req pip1 pip.install '{"python_version":"3.12","packages":[{"name":"six","version":"1.16.0"}]}'
resp pip1            # result.installed[].tier is "mirror" or "relay", with a sha256
"$PY" -c 'import six; print("six", six.__version__)'
```

### 4. Fetch a URL — and watch egress approval

By default an unlisted host goes out through **Popo's** network (the controller
venue), which is gated. Approvals are **persistent** (in
`~/.iceclimber/<sandbox_id>/approvals.json`), so if you (or a previous run) already
approved the host, the fetch just succeeds. To see the approval flow fresh, clear
the rules first on the **host**:

```sh
# --- Terminal A (host) --- start with an empty allow-list so the gate triggers
rm -f ~/.iceclimber/iceclimber-sandbox/approvals.json ~/.iceclimber/iceclimber-sandbox/pending.json
```

```sh
# --- Terminal B (sandbox) ---
req f1 web.fetch '{"url":"https://example.com"}'
resp f1              # {"status":"needs_clarification","clarification":{"question":"... approve f1"}}
```

Now approve it from **Terminal A** (the host), then re-submit:

```sh
# --- Terminal A (host) ---
./iceclimber pending          # shows f1 and the URL
./iceclimber approve f1       # persists a host allow rule

# --- Terminal B (sandbox) ---
req f2 web.fetch '{"url":"https://example.com"}'
resp f2              # {"status":"ok","result":{"status_code":200,"venue":"controller","body_inline":"..."}}
```

Large bodies come back as `"body_blob":"blobs/<sha256>"` — read
`~/.iceclimber/protocol/blobs/<sha256>`.

## Host-side checks (Terminal A)

```sh
./iceclimber status       # heartbeat seq, queue depth, installed runtimes, your capabilities
./iceclimber pending      # controller-venue fetches awaiting approval
./iceclimber web fetch https://example.com   # the same fetch, driven from the host
```

## What to notice

- **Pickup lock** — on service, the request moves `outbox/new` → `outbox/cur`.
- **Effectively-once** — re-submit the *same id* and Popo dedups (no second response).
- **Liveness** — the heartbeat is `"<seq> <timestamp>"`; judge "is Popo alive" on
  the **counter advancing**, not the timestamp (no clock sync needed).
- **Crash recovery** — drop a file straight into `outbox/cur` (no response) and run
  `./iceclimber serve --once`: the recovery sweep services the stranded request.
- **Two venues** — a fetch matching `network.allowed_domains: reachable_from:
  sandbox` (or a `fetch_rewrite` with `venue: sandbox`) runs *in* the sandbox,
  ungated (`venue: sandbox-exec`); everything else is gated controller egress.
- **Audit** — every fetch appends a line to the controller audit log
  (`~/.iceclimber/audit/<sandbox_id>.jsonl`).

## Watch a real agent do it

Everything above is exactly what `NANA.md` instructs. To see an *actual* agent be
Nana, run Claude Code (or any agent) **inside** the VM and give it the output of
`./iceclimber skill print` as its instructions — it will drive the same files.

For a turnkey, end-to-end version of that — a real Claude agent in a **network
air-gapped** sandbox, with `serve` prompting you to approve each operation inline —
see **[`../DEMO.md`](../DEMO.md)**. The short version:

```sh
make demo-up                       # boot + provision the demo VM (once)
# Terminal A: set up + serve, supervised (approve each prompt with y/a/n/d)
make demo-live
# Terminal B: start the agent
make demo-agent
# Optional Terminal C: the merged [POPO]/[NANA] activity feed
make demo-logs
```

Or fully automated (headless, asserts the result): `make demo`. Both need a Claude
subscription token — see DEMO.md's prerequisites.

## Teardown

```sh
make sandbox-down      # stop and delete the VM
```
