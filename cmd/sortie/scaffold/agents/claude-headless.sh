#!/usr/bin/env bash
# Scaffolded by `sortie init` — user-owned; edit freely (sortie never
# overwrites it).
#
# Headless Claude Code agent: reads the resolved step prompt from
# $SORTIE_PROMPT_FILE, runs claude in one-shot stream-json mode, and pipes the
# NDJSON through claude-stream-format.sh, which prints readable log lines and
# writes the final result text to $SORTIE_RESULT_FILE.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

claude --dangerously-skip-permissions \
  --verbose --output-format stream-json \
  -p "$(cat "$SORTIE_PROMPT_FILE")" \
  | "$DIR/claude-stream-format.sh"
