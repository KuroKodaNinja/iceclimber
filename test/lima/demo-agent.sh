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

envfile="$ROOT/agent/claude/env.sh"
if ! limactl shell "$DEMO" -- test -f "$envfile"; then
	echo "agent not installed — run 'iceclimber agent install claude' first (see DEMO.md)" >&2
	exit 1
fi

echo ">>> launching Claude in $DEMO (subscription auth from $envfile; API key emptied; YOLO)"
echo ">>> watch Popo's 'serve' in your other terminal service its requests."

# --verbose streams the agent's tool calls so the operator can watch it work.
prompt="Read the file TASK.md in your current directory and complete the task it
describes, exactly. Start by reading the NANA.md path it points you to."

# Persist the agent's stream so `iceclimber logs --agent-log` can show the [NANA]
# side (it still streams to this terminal). Append so both demo-live passes land
# in one file.
agentlog="$HOME/.iceclimber/$DEMO/agent.log"
mkdir -p "$(dirname "$agentlog")"

# Sourcing the iceclimber-written env file sets the subscription token, empties
# ANTHROPIC_API_KEY (never fall back to metered billing), and puts the runtime node
# on PATH. --max-turns bounds a runaway agent; the three-language task is more Popo
# round-trips than one language, so give it room.
limactl shell "$DEMO" -- \
	bash -lc ". '$envfile' && cd '$ROOT/work' && claude -p \"$prompt\" --dangerously-skip-permissions --verbose --max-turns 150" \
	| tee -a "$agentlog"
