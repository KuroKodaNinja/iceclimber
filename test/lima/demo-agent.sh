#!/usr/bin/env bash
# Launch the Claude agent inside the (air-gapped) demo sandbox to perform the
# acceptance task. Authenticates with the operator's **subscription** token,
# never the metered API.
#
# Usage: test/lima/demo-agent.sh [instance-name]
# Prereqs:
#   - The demo VM is up (make demo-up), bootstrapped, and air-gapped
#     (make demo-firewall); Popo is serving (./iceclimber serve ...).
#   - CLAUDE_CODE_OAUTH_TOKEN is set on the host. Mint it once with
#       claude setup-token
#     This bills your Claude subscription (Pro/Max), not the API.
set -euo pipefail

DEMO="${1:-iceclimber-demo}"
HERE="$(cd "$(dirname "$0")" && pwd)"

: "${CLAUDE_CODE_OAUTH_TOKEN:?set CLAUDE_CODE_OAUTH_TOKEN — run 'claude setup-token' on the host (subscription, NOT an API key)}"

ROOT="$(limactl shell "$DEMO" -- sh -lc 'echo $HOME/iceclimber-demo')"

# Stage the task brief (with the real tree path substituted) next to where the
# agent will work, then point the agent at it.
limactl shell "$DEMO" -- mkdir -p "$ROOT/work"
sed "s#{{ROOT}}#$ROOT#g" "$HERE/../demo/TASK.md" \
	| limactl shell "$DEMO" -- sh -c "cat > '$ROOT/work/TASK.md'"

echo ">>> launching Claude in $DEMO (subscription auth; API key emptied; YOLO)"
echo ">>> watch Popo's 'serve' in your other terminal service its requests."

# ANTHROPIC_API_KEY= ensures we can never silently fall back to metered billing.
# --verbose streams the agent's tool calls so the operator can watch it work.
prompt="Read the file TASK.md in your current directory and complete the task it
describes, exactly. Start by reading the NANA.md path it points you to."

# --max-turns bounds a runaway agent (belt-and-suspenders for CI).
limactl shell "$DEMO" -- \
	env CLAUDE_CODE_OAUTH_TOKEN="$CLAUDE_CODE_OAUTH_TOKEN" ANTHROPIC_API_KEY= \
	bash -lc "cd '$ROOT/work' && claude -p \"$prompt\" --dangerously-skip-permissions --verbose --max-turns 60"
