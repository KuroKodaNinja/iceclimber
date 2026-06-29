You are an autonomous agent running inside a **locked-down sandbox**. This
sandbox has **no internet access**, **no language runtimes**, and **no installed
packages**. You cannot `pip install`, `npm install`, `curl`, or download anything
directly — those will fail.

What you *do* have is **Popo**, a controller running outside the sandbox that will
provision things for you on request. You talk to Popo by reading and writing files
in a maildir-style protocol tree. The full contract — how to format a request,
where to drop it, how to read the response, how to check Popo is alive, and every
available action — is documented here:

    {{ROOT}}/skill/NANA.md

**Read that file first** and follow it exactly. It is the source of truth for the
request/response format and the actions: `ping`, `web.fetch`, `python.install`,
`pip.install`, `node.install`, `npm.install`, `java.install`, `maven.install`.
(The operator may approve requests interactively, so a response can take a few
seconds — keep polling.)

## Your task

Prove Popo bridges **three languages**. First fetch one shared piece of data, then
build a tiny program in **each** of Python, JavaScript, and Java that reads it and
prints a **computed** line (not just an echo of the data).

1. Through Popo, **`web.fetch`** `https://xkcd.com/info.0.json` and save the body to
   `{{ROOT}}/work/comic.json`. (You can't reach the internet; Popo will, on its own
   network.) It contains at least `num` (an integer) and `title` (a string).

2. **Python.** Install **Python 3.12** and the **`rich`** package through Popo.
   Write `{{ROOT}}/work/py/report.py` that reads `comic.json` and prints, using
   `rich`:  `[python] xkcd #<num> title-length=<len(title)>`. Run it with the
   absolute python path `python.install` returned.

3. **JavaScript.** Install **Node 24** and the **`left-pad`** package through Popo.
   Write `{{ROOT}}/work/js/report.js` that reads `comic.json` and prints, using
   `left-pad` to zero-pad the number to 5 digits:
   `[javascript] xkcd #<padded-num> title-length=<title.length>`. Run it with the
   absolute node path, with `NODE_PATH` set to what `npm.install` returned.

4. **Java.** Install **Java 21** through Popo. Write `{{ROOT}}/work/java/Report.java`
   that reads `comic.json` (extract `num` and `title` — a small regex or substring is
   fine, you don't need a JSON library) and prints
   `[java] xkcd #<num> title-length=<title length>`. Run it with the absolute java
   path (Java 21 runs a single `.java` file directly: `<java> Report.java`).

## Done when

All three programs exist and, run with their iceclimber-installed runtimes, each
prints its own `[<lang>] xkcd #<num> title-length=<N>` line — the **same** comic
number, computed from the one payload Popo fetched. The runtimes, the Python and
JavaScript packages, and the data all came through Popo, with no sandbox internet.
