#!/usr/bin/env bash
# Launch the Claude agent inside the (air-gapped) demo sandbox to perform the
# acceptance task. The agent CLI and its subscription auth were installed earlier by
# `iceclimber agent install claude` (the controller relays the agent binary in);
# this just sources the 0600 env file iceclimber wrote (which carries the token and
# empties the API key) and runs the agent.
#
# Usage: test/lima/demo-agent.sh [instance-name]
# Prereqs:
#   - The demo VM is up (make demo-up), bootstrapped, the agent installed
#     (`iceclimber agent install claude`), and air-gapped (make demo-firewall);
#     Popo is serving (./iceclimber serve ...).
set -euo pipefail

DEMO="${1:-iceclimber-demo}"
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"

ROOT="$(limactl shell "$DEMO" -- sh -lc 'echo $HOME/iceclimber-demo')"

# Stage the task brief (with the real tree path substituted) next to where the
# agent will work, then point the agent at it.
limactl shell "$DEMO" -- mkdir -p "$ROOT/work"
sed "s#{{ROOT}}#$ROOT#g" "$HERE/../demo/TASK.md" \
	| limactl shell "$DEMO" -- sh -c "cat > '$ROOT/work/TASK.md'"

nana="$ROOT/nana"
if ! limactl shell "$DEMO" -- test -x "$nana"; then
	echo "agent not installed — run 'iceclimber agent install claude' first (see DEMO.md)" >&2
	exit 1
fi

echo ">>> launching Claude in $DEMO via the nana launcher (subscription auth; API key emptied; YOLO)"
echo ">>> watch Popo's 'serve' in your other terminal service its requests."

# TASK.md uses absolute {{ROOT}}/work paths, so no cwd dependency. The nana launcher
# sources the agent's env (token, API key emptied) and injects NANA.md as the agent's
# system context, so the prompt only needs to hand over the task.
prompt="Read $ROOT/work/TASK.md and complete the task it describes, exactly."

# nana wraps the agent: it picks the sole installed agent (claude), sets up auth +
# NANA.md, and passes these flags through to it. With -p (a headless print run), nana
# auto-injects `--output-format stream-json --verbose` (so each turn emits one JSON
# event the bridge renders into [NANA] tool calls, not just the final summary) and
# mirrors the stream to the sandbox session.log, which the serving Popo bridges to the
# controller so `iceclimber logs`/`tui`/the console show it with no --agent-log. We pass
# no --output-format here on purpose — dogfooding that injection. (A caller's own
# --output-format always wins.)
#   --max-turns bounds a runaway agent (the three-language task is more Popo round-trips
#     than one language, so give it room).
limactl shell "$DEMO" -- \
	"$nana" -p "$prompt" --dangerously-skip-permissions --max-turns 150
