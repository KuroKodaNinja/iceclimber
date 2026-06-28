# Functional tests

Black-box end-to-end tests that drive the **real `iceclimber` binary** against a
real sandbox — a [Lima](https://lima-vm.io) VM running **Alpine Linux**. They are
the low-frequency, real-dependency regression suite; the fast unit suite lives
next to the code under `internal/`.

## Why Alpine

Alpine is **musl libc + BusyBox**, deliberately. It stresses the two things the
design bets on harder than any glibc/coreutils distro would:

- **musl detection** — exercises the `/lib/ld-musl-*` branch of `probe`'s
  multi-signal libc detection.
- **POSIX-sh discipline** — BusyBox `df`/`ls`/`sh`/`head`/`printf` are stricter
  than GNU coreutils, so "no bashisms, no GNU-only flags" (plan §6, §7) is
  actually enforced. If a probe command works here, it works anywhere.

## Prerequisites

- [Lima](https://lima-vm.io) (`limactl`) — on Apple Silicon the VM uses the
  built-in `vz` backend, so no QEMU is needed.
- `ssh-keyscan` (ships with OpenSSH).

## Running

```sh
make sandbox-up        # boot the Alpine VM (first run downloads the image)
make test-functional   # build the binary + run the tagged tests against it
make sandbox-down       # stop and delete the VM when done
# or, one-shot:
make e2e               # sandbox-up + test-functional
```

The tests are gated behind the `functional` build tag, so plain `go test ./...`
never touches Lima. If the VM isn't running they **skip** with a message telling
you to `make sandbox-up` — boot once, iterate often.

## How it works

- The Makefile owns the VM lifecycle; tests only connect (`test/functional/lima_test.go`).
- The harness discovers the running instance via `limactl list --json`, reads SSH
  params from Lima's generated `ssh.config`, and `ssh-keyscan`s the VM's current
  host key into a temp `known_hosts` (Lima regenerates host keys per VM).
- It writes a real `iceclimber.yaml` pointing at the VM and runs the built binary,
  asserting on `probe --json`. This exercises the full user path:
  config → CLI → SSH dial (with **strict** host-key verification) → probe.
- `TestProbe_RejectsUnknownHostKey` confirms an unknown host is rejected rather
  than trusted on first use.

## Application scenarios

Beyond these per-feature tests, [`scenarios/`](scenarios/) holds **full-stack
"build a real application" tests** — one self-contained directory per language that
provisions a runtime + packages, fetches data through Popo, and builds/runs a real
program in the sandbox (the scripted equivalent of the agent demo). They use a
separate `scenario` build tag; run them with `make scenario`. Each scenario's
operating notes live in its own directory's `README.md` — see
[`scenarios/README.md`](scenarios/README.md).

## Not covered yet

**Network-boundary simulation is deferred.** A real sandbox has no general egress
and reaches an internal mirror; a plain Lima VM has full internet. Modeling that
(firewalling the VM, standing up a mock mirror) lands with the pip-tier phases
(plan §5, phases 4–5), where it first matters.
