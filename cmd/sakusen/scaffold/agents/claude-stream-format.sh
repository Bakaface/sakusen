#!/usr/bin/env bash
# Scaffolded by `sakusen init` — user-owned; edit freely.
#
# Formats Claude Code `--output-format stream-json` NDJSON (on stdin) into
# readable log lines and writes the final result text to $SAKUSEN_RESULT_FILE.
# Sakusen itself never parses this output — the log lines are for humans, and
# the result file is the only contract. Requires jq.
set -uo pipefail

: "${SAKUSEN_RESULT_FILE:=/dev/null}"

while IFS= read -r line; do
  type=$(printf '%s' "$line" | jq -r '.type // empty' 2>/dev/null) || continue
  case "$type" in
    assistant)
      # Text and tool calls in message order; each tool call shows its most
      # telling argument (command, path, pattern, ...), first line only.
      printf '%s' "$line" | jq -r '
        .message.content[]? |
        if .type == "text" then .text
        elif .type == "tool_use" then
          ([.input.command, .input.file_path, .input.path, .input.pattern,
            .input.url, .input.query, .input.description, .input.skill,
            .input.prompt]
           | map(select(type == "string" and length > 0)) | first // "") as $arg |
          ($arg | (split("\n")[0] // "") | if length > 120 then .[:120] + "…" else . end) as $arg |
          "Tool: " + .name + (if $arg == "" then "" else " — " + $arg end)
        else empty end' 2>/dev/null
      ;;
    result)
      printf '%s' "$line" | jq -r '.result // empty' > "$SAKUSEN_RESULT_FILE"
      printf '%s' "$line" | jq -r '(if .is_error then "Failed" else "Done" end) + " (" + (((.duration_ms // 0) / 1000 * 10 | round) / 10 | tostring) + "s, $" + ((.total_cost_usd // 0) | tostring) + ")"' 2>/dev/null
      ;;
  esac
done
