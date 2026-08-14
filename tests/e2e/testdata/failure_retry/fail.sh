#!/usr/bin/env bash
# Stub agent command that always fails (simulates a broken agent run).
set -euo pipefail
TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
PURPOSE="${SORTIE_PURPOSE:-step}"
STEP="${SORTIE_STEP:-}"
if [[ -n "${SORTIE_E2E_LOG:-}" ]]; then
    printf '%s\t%s\t%s\t%s\t\n' "$TIMESTAMP" "$PURPOSE" "$(pwd)" "$STEP" >> "$SORTIE_E2E_LOG"
fi
echo "simulated agent failure" >&2
exit 1
