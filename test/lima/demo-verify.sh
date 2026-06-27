#!/usr/bin/env bash
# Verify the agent's work in the demo sandbox: prove that the program it built
# actually renders the data it fetched through Popo.
#
# Usage: test/lima/demo-verify.sh [instance-name]
#
# Checks, against the VM:
#   1. work/comic.json exists and is a real xkcd payload (has num + title).
#   2. work/comics.py runs under the iceclimber-installed Python (exit 0).
#   3. its output contains that comic's number and title — i.e. python +
#      rich + the relayed data all came together through Popo.
set -euo pipefail

DEMO="${1:-iceclimber-demo}"
ROOT="$(limactl shell "$DEMO" -- sh -lc 'echo $HOME/iceclimber-demo')"
PY="$ROOT/runtimes/python/3.12/bin/python3"
WORK="$ROOT/work"

fail() { echo "DEMO VERIFY: FAIL — $1" >&2; exit 1; }

# 1. The fetched payload.
json="$(limactl shell "$DEMO" -- cat "$WORK/comic.json" 2>/dev/null)" \
	|| fail "no $WORK/comic.json (did the agent web.fetch it through Popo?)"
read -r NUM TITLE < <(printf '%s' "$json" | python3 -c '
import json,sys
d=json.load(sys.stdin)
print(d["num"], d["title"])
') || fail "comic.json is not a valid xkcd payload"
[ -n "$NUM" ] && [ -n "$TITLE" ] || fail "comic.json missing num/title"
echo "DEMO VERIFY: fetched comic #$NUM — \"$TITLE\""

# 2 + 3. Run the program and check it renders that comic.
out="$(limactl shell "$DEMO" -- "$PY" "$WORK/comics.py" 2>&1)" \
	|| fail "comics.py did not run cleanly under $PY:
$out"
case "$out" in
	*"$NUM"*) ;; *) fail "program output is missing comic number $NUM:
$out" ;;
esac
case "$out" in
	*"$TITLE"*) ;; *) fail "program output is missing title \"$TITLE\":
$out" ;;
esac

echo "DEMO VERIFY: PASS — comics.py rendered comic #$NUM \"$TITLE\" using the"
echo "  iceclimber-installed Python + rich, from data fetched through Popo."
