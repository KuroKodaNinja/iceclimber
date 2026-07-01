# Python application scenarios

Two full-stack Python scenarios that build and run real programs in the sandbox, the
way the agent would — covering both package managers and both relay flows.

## `TestPythonApp` — pip relay (musl box)

1. **`web.fetch` through Popo** — fetches the xkcd comic JSON (sandbox venue).
2. **`python.install`** — a portable Python (python-build-standalone).
3. **`pip.install pandas` (Tier 1 relay)** — pandas + numpy + deps, downloaded by Popo
   as tag-matched **musllinux** wheels and relayed in (real C-extension packages).
4. **run** — `app/app.py` processes the comic with pandas/numpy and prints `PANDAS_OK`
   plus the computed report.
5. **assert** — output carries `PANDAS_OK`, the comic number, title, and title length.

## `TestPythonCondaApp` — conda relay (glibc box)

A **real conda project** (`app/environment.yml` pinning `python` + `pytorch` + `pandas`
+ `numpy`) built by the **air-gapped conda relay**:

1. **`web.fetch`** — same comic input.
2. **`conda.install --file environment.yml --tier relay`** — the controller's
   conda/mamba solves the env for the sandbox platform, downloads it, synthesizes a
   local channel, and the sandbox **creates the env offline** at `<root>/envs/mlkit`.
3. **run** — `app/ml.py` does a small PyTorch tensor computation, tabulates it with
   pandas/numpy, and prints `MLKIT_OK` plus the computed report.
4. **assert** — output carries `MLKIT_OK`, the comic number, title, and title length.

PyTorch has no musl build, so this runs on the glibc box and **skips** without
conda/mamba on the controller (the relay's solve/download engine).

## Running

```sh
make sandbox-up && make sandbox-glibc-up   # both boxes (glibc for the conda scenario)
make scenario                              # runs every test/scenarios/<lang> scenario
# or just these:
go test -tags scenario -run 'TestPythonApp|TestPythonCondaApp' ./test/scenarios/python/...
```

`app/app.py`, `app/ml.py`, and `app/environment.yml` are the applications; everything
else is the harness driving the real `iceclimber` binary against the VMs.
