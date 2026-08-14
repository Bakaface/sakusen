#!/usr/bin/env bash
# Counter agent command for the loop_exits scenario.
# Workflow: implementing → checking [loop back if checking non-empty].
# To exercise loop_iteration >= 1, we need implementing+checking to return
# non-empty for the first pass (iteration 0), then iteration 1's checking
# returns empty to exit the loop.
#
# Call sequence (count = 0-indexed before increment):
#   count=0: implementing  → non-empty (iteration 0)
#   count=1: checking      → non-empty (iteration 0) → triggers loop
#   count=2: implementing  → non-empty (iteration 1)
#   count=3+: checking     → empty                   → loop exits

set -euo pipefail

STATE_FILE="${E2E_RESPONSES_DIR}/counter.state"
COUNT=0
if [[ -f "$STATE_FILE" ]]; then
    COUNT=$(cat "$STATE_FILE")
fi

# Log the call
TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
PURPOSE="${SORTIE_PURPOSE:-step}"
STEP="${SORTIE_STEP:-}"
if [[ -n "${SORTIE_E2E_LOG:-}" ]]; then
    printf '%s\t%s\t%s\t%s\t\n' "$TIMESTAMP" "$PURPOSE" "$(pwd)" "$STEP" >> "$SORTIE_E2E_LOG"
fi

NEXT_COUNT=$((COUNT + 1))
printf '%d' "$NEXT_COUNT" > "$STATE_FILE"

if [[ "$COUNT" -lt 3 ]]; then
    # implementing+checking iter 0, implementing iter 1 — return non-empty
    printf 'work done %d' "$COUNT" > "${SORTIE_RESULT_FILE:?}"
    printf 'work done %d\n' "$COUNT"
else
    # checking iter 1+ — empty result triggers loop exit
    : > "${SORTIE_RESULT_FILE:?}"
fi
