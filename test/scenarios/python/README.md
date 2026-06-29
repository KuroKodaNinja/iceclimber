# Python application scenario

A self-contained, full-stack scenario that builds and runs a real **Python** program
in the sandbox, the way the agent would — the Python counterpart of the Node and
Java scenarios.

What it exercises, end to end:

1. **`web.fetch` through Popo** — fetches the xkcd comic JSON (sandbox venue).
2. **`python.install`** — provisions a portable Python (python-build-standalone).
3. **`pip.install`** — installs **rich** (Tier 1 relay: Popo downloads on its network
   and relays the wheel in).
4. **run** — `app/app.py` uses rich to render a computed report from the fetched
   comic and prints `[python] xkcd #<num> <title> title-length=<N>`.
5. **assert** — the output carries the comic number, title, and computed title
   length, proving the program ran, rich loaded, and it processed the fetched data.

Run it (needs the Lima sandbox up):

```sh
make scenario            # runs every test/scenarios/<lang> scenario
# or just this one:
go test -tags scenario -run TestPythonApp ./test/scenarios/python/...
```

`app/app.py` is the application; everything else is the harness driving the real
`iceclimber` binary against the VM.
