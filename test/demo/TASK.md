You are an autonomous agent running inside a **locked-down sandbox**. This
sandbox has **no internet access**, **no Python**, and **no installed packages**.
You cannot `pip install`, `curl`, or download anything directly — those will
fail.

What you *do* have is **Popo**, a controller running outside the sandbox that will
provision things for you on request. You talk to Popo by reading and writing files
in a maildir-style protocol tree. The full contract — how to format a request,
where to drop it, how to read the response, how to check Popo is alive — is
documented here:

    {{ROOT}}/skill/NANA.md

**Read that file first** and follow it exactly. It is the source of truth for the
request/response format and the available actions (`ping`, `python.install`,
`pip.install`, `web.fetch`).

## Your task

Build and run a small program that prints today's xkcd comic.

1. Through Popo, install **Python 3.12**.
2. Through Popo, install the **`rich`** package for that Python.
3. Through Popo, **fetch** the JSON at `https://xkcd.com/info.0.json` (you can't
   reach it directly — Popo will, on its own network). Save the body to
   `{{ROOT}}/work/comic.json`.
   - If the fetch comes back `needs_clarification`, an operator must approve it on
     the Popo side. Relay the request, and once it's approved re-submit as a
     **new request with a new id** for the same URL (per NANA.md — the original id
     already has a response and will not be re-serviced).
4. Write a Python program at `{{ROOT}}/work/comics.py` that reads
   `{{ROOT}}/work/comic.json` and uses the `rich` library to print the comic's
   **number** (`num`) and **title** (`title`) inside a `rich.panel.Panel`.
5. **Run** the program using the absolute Python path that `python.install`
   returned (do not rely on `PATH`), and show its output.

## Done when

`{{ROOT}}/work/comics.py` exists and, when run with the installed Python, prints
the current comic's number and title in a panel. Everything you needed — the
runtime, the package, and the data — came through Popo.
