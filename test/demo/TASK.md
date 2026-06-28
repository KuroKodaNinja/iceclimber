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
`pip.install`, `web.fetch`). (Note: the operator may approve each request
interactively, so a response can take a few seconds — keep polling.)

## Your task

Build and run a small program that turns today's xkcd comic into a **computed,
clearly-generated report** — not just an echo of the fetched data.

1. Through Popo, install **Python 3.12**.
2. Through Popo, install **both** packages in one request: **`rich`** and
   **`pyfiglet`** (for that Python).
3. Through Popo, **fetch** the JSON at `https://xkcd.com/info.0.json` (you can't
   reach it directly — Popo will, on its own network). Save the body to
   `{{ROOT}}/work/comic.json`.
4. Write a Python program at `{{ROOT}}/work/comics.py` that reads
   `{{ROOT}}/work/comic.json` and **computes and renders** a report. It must:
   - use **`pyfiglet`** to print an ASCII-art banner of `xkcd #<num>`;
   - **compute** statistics from the data — at minimum the title's length in
     characters (`len(title)`) and the number of words in the `alt` text — and
     print them in a **`rich` table** (clearly label the title character count);
   - render a small **bar chart** (e.g. a histogram of word lengths in the `alt`
     text) using `rich` (block characters like `█` are fine);
   - use `rich` for the panel/table/colour so the output is obviously program-built.
5. **Run** the program using the absolute Python path that `python.install`
   returned (do not rely on `PATH`), and show its output.

## Done when

`{{ROOT}}/work/comics.py` exists and, when run with the installed Python, prints
the ASCII banner, a stats table including the title's character count, and the bar
chart — all derived from the data Popo fetched. Everything you needed — the
runtime, both packages, and the data — came through Popo.
