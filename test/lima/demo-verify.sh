#!/usr/bin/env bash
# Verify the agent's work in the demo sandbox: prove that the program it built
# actually renders the data it fetched through Popo.
#
# Usage: test/lima/demo-verify.sh [instance-name]
#
# Checks, against the VM:
#   1. work/comic.json exists and is a real xkcd payload (has num + title).
#   2. work/comics.py imports BOTH rich and pyfiglet (the two relayed libs).
#   3. work/comics.py runs under the iceclimber-installed Python (exit 0).
#   4. its output contains the comic's number, title, AND the recomputed title
#      length in characters — proving real computation (not an echo), two libs,
#      and the relayed data all came together through Popo.
set -euo pipefail

DEMO="${1:-iceclimber-demo}"
ROOT="$(limactl shell "$DEMO" -- sh -lc 'echo $HOME/iceclimber-demo')"
WORK="$ROOT/work"

fail() { echo "DEMO VERIFY: FAIL — $1" >&2; exit 1; }

# Discover the installed interpreter — the runtime dir is named for the resolved
# version + platform (e.g. 3.12.13-aarch64-musl), not the requested "3.12".
PY="$(limactl shell "$DEMO" -- sh -lc "ls $ROOT/runtimes/python/*/bin/python3 2>/dev/null | head -1")"
[ -n "$PY" ] || fail "no installed Python under $ROOT/runtimes/python (did python.install run?)"

# 1. The fetched payload (num, title length, title).
json="$(limactl shell "$DEMO" -- cat "$WORK/comic.json" 2>/dev/null)" \
	|| fail "no $WORK/comic.json (did the agent web.fetch it through Popo?)"
read -r NUM TLEN TITLE < <(printf '%s' "$json" | python3 -c '
import json,sys
d=json.load(sys.stdin)
print(d["num"], len(d["title"]), d["title"])
') || fail "comic.json is not a valid xkcd payload"
[ -n "$NUM" ] && [ -n "$TITLE" ] || fail "comic.json missing num/title"
echo "DEMO VERIFY: fetched comic #$NUM — \"$TITLE\" (title is $TLEN chars)"

# 2. The program uses both relayed libraries.
src="$(limactl shell "$DEMO" -- cat "$WORK/comics.py" 2>/dev/null)" \
	|| fail "no $WORK/comics.py"
case "$src" in *pyfiglet*) ;; *) fail "comics.py does not use pyfiglet" ;; esac
case "$src" in *rich*) ;; *) fail "comics.py does not use rich" ;; esac

# 3 + 4. Run it; require the number, the title, and the computed title length.
out="$(limactl shell "$DEMO" -- "$PY" "$WORK/comics.py" 2>&1)" \
	|| fail "comics.py did not run cleanly under $PY:
$out"
for want in "$NUM" "$TITLE" "$TLEN"; do
	case "$out" in
		*"$want"*) ;;
		*) fail "program output is missing \"$want\":
$out" ;;
	esac
done

echo "DEMO VERIFY: PASS — comics.py (rich + pyfiglet) rendered comic #$NUM \"$TITLE\""
echo "  with a computed title length of $TLEN chars, from data fetched through Popo."
