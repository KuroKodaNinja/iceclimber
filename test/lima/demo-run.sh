#!/usr/bin/env bash
# Hands-off acceptance run: a real Claude agent in the air-gapped demo sandbox
# provisions Python + a package + web data through Popo, builds a program, and
# runs it — and we assert the result. This is the body of `make demo` (CI).
#
# Unlike the live walkthrough (DEMO.md), there's no human at the egress gate, so
# we PRE-APPROVE the fetch host up front (an operator-owned allow rule). The gate
# is still enforced — it's just pre-satisfied, exactly as an operator would do
# before an unattended run.
#
# Usage: test/lima/demo-run.sh [instance-name]
# Requires: the demo VM up + provisioned, the iceclimber binary built, and
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

# 1. Point a config at the VM (remote_root pinned) and create the tree.
root="$(limactl shell "$DEMO" -- sh -lc 'echo $HOME/iceclimber-demo')"
bash "$HERE/gen-config.sh" "$DEMO" "$CFG" "$root"
"$BIN" bootstrap --config "$CFG"

# 2. Pre-approve the fetch host (operator-owned allow rule) so the unattended
#    agent isn't held at the gate.
approvals="$HOME/.iceclimber/$DEMO/approvals.json"
mkdir -p "$(dirname "$approvals")"
printf '%s\n' '{"allow":["https://xkcd.com/*"],"deny":[]}' > "$approvals"

# 3. Air-gap the sandbox. Restore egress on exit, whatever happens.
cleanup() {
	[ -n "${SERVE_PID:-}" ] && kill "$SERVE_PID" 2>/dev/null || true
	limactl shell "$DEMO" -- sudo sh -s down < "$HERE/demo-firewall.sh" >/dev/null 2>&1 || true
}
trap cleanup EXIT
limactl shell "$DEMO" -- sudo sh -s up < "$HERE/demo-firewall.sh"

# 4. Popo serves in the background — the only thing the sandbox can reach besides
#    its own API.
"$BIN" serve --config "$CFG" &
SERVE_PID=$!
sleep 2

# 5. Clean maildir, then run the agent (one headless pass; the fetch is allowed).
make -C "$REPO" demo-reset
bash "$HERE/demo-agent.sh" "$DEMO"

# 6. Assert the agent's program renders the data it fetched through Popo.
bash "$HERE/demo-verify.sh" "$DEMO"
