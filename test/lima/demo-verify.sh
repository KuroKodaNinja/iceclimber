#!/usr/bin/env bash
# Verify the agent's work in the demo sandbox: prove that the programs it built —
# one each in Python, JavaScript, and Java — actually ran under the
# iceclimber-installed runtimes and computed values from the data Popo fetched.
#
# Usage: test/lima/demo-verify.sh [instance-name]
#
# Checks, against the VM:
#   1. work/comic.json exists and is a real xkcd payload (num + title).
#   2. for each language, the program file exists, runs under its installed runtime
#      (exit 0), and its output carries that language's tag, the comic number, AND
#      the computed title length — proving a real program ran (not an echo), the
#      runtime + packages + data all came together through Popo, for all three.
set -euo pipefail

DEMO="${1:-iceclimber-demo}"
ICECLIMBER_HOME="$(limactl shell "$DEMO" -- sh -lc 'echo $HOME/iceclimber-demo')"
WORK="$ICECLIMBER_HOME/work"

fail() { echo "DEMO VERIFY: FAIL — $1" >&2; exit 1; }

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

# vm_glob: first match of a glob inside the VM (empty if none). `-d` lists the
# matched path itself, not a directory's contents — so a node_modules glob returns
# the directory (for NODE_PATH), not its first entry.
vm_glob() { limactl shell "$DEMO" -- sh -lc "ls -d $1 2>/dev/null | head -1"; }

# check <label> <program-path> <run-command>: require the program exists, runs
# cleanly, and its output carries the language tag, the comic number, and the
# computed title length.
check() {
	local label="$1" prog="$2" runcmd="$3"
	limactl shell "$DEMO" -- test -f "$prog" || fail "$label: missing $prog"
	local out
	out="$(limactl shell "$DEMO" -- sh -lc "$runcmd" 2>&1)" \
		|| fail "$label: program did not run cleanly:
$out"
	for want in "[$label]" "$NUM" "$TLEN"; do
		case "$out" in
			*"$want"*) ;;
			*) fail "$label output is missing \"$want\":
$out" ;;
		esac
	done
	echo "DEMO VERIFY: $label OK — $out"
}

# 2a. Python (rich) under the installed interpreter.
PY="$(vm_glob "$ICECLIMBER_HOME/runtimes/python/*/bin/python3")"
[ -n "$PY" ] || fail "no installed Python (did python.install run?)"
check python "$WORK/py/report.py" "$PY $WORK/py/report.py"

# 2b. JavaScript (left-pad) under the installed Node, with NODE_PATH set.
NODE="$(vm_glob "$ICECLIMBER_HOME/runtimes/node/*/bin/node")"
[ -n "$NODE" ] || fail "no installed Node (did node.install run?)"
NMOD="$(vm_glob "$ICECLIMBER_HOME/runtimes/node/*/lib/node_modules")"
check javascript "$WORK/js/report.js" "NODE_PATH=$NMOD $NODE $WORK/js/report.js"

# 2c. Java under the installed JDK (single-file source launch).
JAVA="$(vm_glob "$ICECLIMBER_HOME/runtimes/java/*/bin/java")"
[ -n "$JAVA" ] || fail "no installed Java (did java.install run?)"
check java "$WORK/java/Report.java" "$JAVA $WORK/java/Report.java"

echo "DEMO VERIFY: PASS — Python, JavaScript, and Java each computed comic #$NUM"
echo "  (title length $TLEN), with runtimes, packages, and data all bridged through Popo."
