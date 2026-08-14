#!/usr/bin/env bash
# Scaffolded by `sortie init` — user-owned; edit freely.
#
# Prints the step's chat transcript on stdout for sortie's summarize_chat
# strategy. Sortie runs this in the task workdir with:
#   SORTIE_TRANSCRIPT_PATH  transcript path from the latest turn-end sentinel
#                           (Claude's Stop-hook payload), when available
#   SORTIE_SESSION_ID       recorded agent session id, when available
# Falls back to locating the session JSONL under ~/.claude/projects/.
# Requires jq. Printing nothing means "no chat content" (sortie skips the
# summary with a warning, or fails the step when require_context is set).
set -uo pipefail

transcript="${SORTIE_TRANSCRIPT_PATH:-}"
if [ -z "$transcript" ] && [ -n "${SORTIE_SESSION_ID:-}" ]; then
  # Claude Code encodes the workdir path by replacing every non-alphanumeric
  # character with '-'.
  encoded=$(printf '%s' "$PWD" | sed 's/[^a-zA-Z0-9]/-/g')
  candidate="$HOME/.claude/projects/$encoded/$SORTIE_SESSION_ID.jsonl"
  [ -f "$candidate" ] && transcript="$candidate"
fi

[ -n "$transcript" ] && [ -f "$transcript" ] || exit 0

# A session with no assistant turn holds only the injected prompt — feeding it
# to the summarizer makes it re-enact the prompt instead of summarizing.
grep -q '"type":"assistant"' "$transcript" || exit 0

jq -r '
  select((.type == "user" or .type == "assistant") and .message != null) |
  .type as $role |
  (if (.message.content | type) == "string" then .message.content
   else ([.message.content[]? | select(.type == "text") | .text] | join("\n"))
   end) as $text |
  select(($text | length) > 0) |
  ($role + ": " + $text + "\n")
' "$transcript" 2>/dev/null
