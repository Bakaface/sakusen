#!/usr/bin/env bash
# Scaffolded by `sortie init` — user-owned; edit freely.
#
# Interactive Claude Code agent for tmux-mode steps. Layers the Stop hook in
# claude-settings.json onto the session via --settings: the hook writes a
# turn-end sentinel into $SORTIE_DONE_DIR after every completed turn, which
# sortie uses to auto-advance the workflow and to record the session id
# (resume + chat-log lookup). Without the hook, tmux steps are manual-advance.
set -uo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

ARGS=(--dangerously-skip-permissions --settings "$DIR/claude-settings.json")

if [ -s "${SORTIE_PROMPT_FILE:-}" ]; then
  exec claude "${ARGS[@]}" "$(cat "$SORTIE_PROMPT_FILE")"
else
  exec claude "${ARGS[@]}"
fi
