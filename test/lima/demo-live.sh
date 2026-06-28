#!/usr/bin/env bash
# Operator-driven acceptance demo: watch a real Claude agent in the air-gapped
# sandbox bridge through Popo, and approve each operation inline — at the serve
# prompt — with your own eyes.
#
# Unlike `make demo` (headless, pre-approved, asserts), this runs `serve` in the
# FOREGROUND as a supervised session: it pauses before each install and fetch and
# you approve right there. Inline approval returns the real result in one pass, so
# there's no two-pass dance.
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
CFG="$REPO/.demo/config.yaml"
[ -x "$BIN" ] || { echo "build the binary first: make build" >&2; exit 1; }

# 1. Stage the tree + a fresh, ungated egress gate + a clean maildir.
root="$(limactl shell "$DEMO" -- sh -lc 'echo $HOME/iceclimber-demo')"
bash "$HERE/gen-config.sh" "$DEMO" "$CFG" "$root"
"$BIN" bootstrap --config "$CFG"
rm -f "$HOME/.iceclimber/$DEMO/approvals.json" "$HOME/.iceclimber/$DEMO/pending.json"
: > "$HOME/.iceclimber/$DEMO/agent.log" 2>/dev/null || true
make -C "$REPO" demo-reset

# 2. Air-gap the sandbox. Restore egress on exit.
cleanup() {
	limactl shell "$DEMO" -- sudo sh -s down < "$HERE/demo-firewall.sh" >/dev/null 2>&1 || true
	echo "(egress restored)"
}
trap cleanup EXIT
limactl shell "$DEMO" -- sudo sh -s up < "$HERE/demo-firewall.sh"

cat <<BANNER

================================================================================
 Popo is about to serve, SUPERVISED — it will pause for you to approve each
 operation (install / fetch) the agent requests.

 In a SECOND terminal, start the agent:

     make demo-agent

 Approve each prompt below (y / a / n / d). When the agent has finished in the
 other terminal, press Ctrl-C HERE to stop serving and verify the result.
 (Optional: a THIRD terminal can run  make demo-logs  for the merged feed.)
================================================================================

BANNER

# 3. Serve in the foreground, interactive. Ctrl-C stops it; then we verify.
"$BIN" serve --config "$CFG" || true

echo
bash "$HERE/demo-verify.sh" "$DEMO"
