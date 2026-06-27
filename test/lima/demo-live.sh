#!/usr/bin/env bash
# Operator-driven acceptance demo: watch a real Claude agent in the air-gapped
# sandbox bridge through Popo, and approve its egress with your own eyes.
#
# Unlike `make demo` (headless, pre-approved, asserts), this keeps you in the
# loop at the egress gate. A one-shot headless `claude -p` can't wait for an
# out-of-band approval, so the flow is two passes: pass 1 provisions and stops at
# the gate; you approve; pass 2 completes.
#
# Usage: test/lima/demo-live.sh [instance-name]
# Requires: demo VM up + provisioned, the binary built, and
#   CLAUDE_CODE_OAUTH_TOKEN set (subscription; see DEMO.md).
set -euo pipefail

DEMO="${1:-iceclimber-demo}"
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
cd "$REPO"

BIN="$REPO/iceclimber"
CFG="$REPO/iceclimber-demo.yaml"
[ -x "$BIN" ] || { echo "build the binary first: make build" >&2; exit 1; }
: "${CLAUDE_CODE_OAUTH_TOKEN:?set CLAUDE_CODE_OAUTH_TOKEN — run 'claude setup-token' (subscription, not API)}"

# 1. Stage the tree + a fresh, ungated egress gate (so you witness the hold).
root="$(limactl shell "$DEMO" -- sh -lc 'echo $HOME/iceclimber-demo')"
bash "$HERE/gen-config.sh" "$DEMO" "$CFG" "$root"
"$BIN" bootstrap --config "$CFG"
rm -f "$HOME/.iceclimber/$DEMO/approvals.json" "$HOME/.iceclimber/$DEMO/pending.json"
make -C "$REPO" demo-reset

# 2. Air-gap + serve in the background. Restore egress / stop serve on exit.
cleanup() {
	[ -n "${SERVE_PID:-}" ] && kill "$SERVE_PID" 2>/dev/null || true
	limactl shell "$DEMO" -- sudo sh -s down < "$HERE/demo-firewall.sh" >/dev/null 2>&1 || true
	echo "(egress restored, serve stopped)"
}
trap cleanup EXIT
limactl shell "$DEMO" -- sudo sh -s up < "$HERE/demo-firewall.sh"
"$BIN" serve --config "$CFG" >/tmp/iceclimber-demo-serve.log 2>&1 &
SERVE_PID=$!
sleep 2

cat <<BANNER

================================================================================
 PASS 1 — watch the agent provision through Popo, then hit the egress gate.
 (Popo's serve is logging to /tmp/iceclimber-demo-serve.log)
================================================================================
BANNER
bash "$HERE/demo-agent.sh" "$DEMO" || true

cat <<BANNER

================================================================================
 The agent's web.fetch was HELD at the gate — it cannot reach the network
 without your approval. In ANOTHER terminal, approve it:

     ./iceclimber pending --config iceclimber-demo.yaml
     ./iceclimber approve <id> --config iceclimber-demo.yaml
================================================================================
BANNER
printf 'Press Enter once you have approved the fetch... '
read -r _

# 3. Pass 2: clean maildir (dedup won't re-service a held id), re-run, verify.
make -C "$REPO" demo-reset
cat <<BANNER

================================================================================
 PASS 2 — fetch now allowed; the agent finishes the job.
================================================================================
BANNER
bash "$HERE/demo-agent.sh" "$DEMO"

echo
bash "$HERE/demo-verify.sh" "$DEMO"
