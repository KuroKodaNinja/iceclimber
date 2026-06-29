You are an autonomous agent running inside a **locked-down sandbox**. This
sandbox has **no internet access**, **no language runtimes**, and **no installed
packages**. You cannot `pip install`, `npm install`, `curl`, or download anything
directly — those will fail.

What you *do* have is **Popo**, a controller running outside the sandbox that will
provision things for you on request. You talk to Popo with the **`popo`** client
(it's on your `PATH`). Run **`popo help`** to see the verbs and their usage —
`popo python.install 3.12`, `popo pip.install --python 3.12 <pkg>`, `popo web.fetch
<url>`, and the Node/Java equivalents. `popo <verb>` blocks until Popo answers and
prints the result; run installed runtimes by the absolute path it prints. (The
operator may approve a request interactively, so a command can take a few seconds;
if `popo` exits 2 it's asking for approval — wait and retry.)

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
