#!/usr/bin/env bash
# Scaffolded by `sakusen init` — user-owned; edit freely (sakusen never
# overwrites it).
#
# Headless Claude Code agent: reads the resolved step prompt from
# $SAKUSEN_PROMPT_FILE, runs claude in one-shot stream-json mode, and pipes the
# NDJSON through claude-stream-format.sh, which prints readable log lines and
# writes the final result text to $SAKUSEN_RESULT_FILE.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

claude --dangerously-skip-permissions \
  --verbose --output-format stream-json \
  -p "$(cat "$SAKUSEN_PROMPT_FILE")" \
  | "$DIR/claude-stream-format.sh"
