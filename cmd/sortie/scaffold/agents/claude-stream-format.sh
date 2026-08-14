#!/usr/bin/env bash
# Scaffolded by `sortie init` — user-owned; edit freely.
#
# Formats Claude Code `--output-format stream-json` NDJSON (on stdin) into
# readable log lines and writes the final result text to $SORTIE_RESULT_FILE.
# Sortie itself never parses this output — the log lines are for humans, and
# the result file is the only contract. Requires jq.
set -uo pipefail

: "${SORTIE_RESULT_FILE:=/dev/null}"

while IFS= read -r line; do
  type=$(printf '%s' "$line" | jq -r '.type // empty' 2>/dev/null) || continue
  case "$type" in
    assistant)
      printf '%s' "$line" | jq -r '.message.content[]? | select(.type == "text") | .text' 2>/dev/null
      printf '%s' "$line" | jq -r '.message.content[]? | select(.type == "tool_use") | "Tool: " + .name' 2>/dev/null
      ;;
    result)
      printf '%s' "$line" | jq -r '.result // empty' > "$SORTIE_RESULT_FILE"
      printf '%s' "$line" | jq -r '"Done (" + (((.duration_ms // 0) / 1000 * 10 | round) / 10 | tostring) + "s, $" + ((.total_cost_usd // 0) | tostring) + ")"' 2>/dev/null
      ;;
  esac
done
