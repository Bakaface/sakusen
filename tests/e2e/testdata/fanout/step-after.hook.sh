#!/usr/bin/env bash
# Records what the parent sees once its fanout joined, then adds its own file
# so the parent's merge into main has a diff.

set -euo pipefail

ls "$SAKUSEN_WORKTREE" > "$E2E_PROMPT_CAPTURE_DIR/after-files.txt"
git -C "$SAKUSEN_PROJECT_PATH" rev-list --count main > "$E2E_PROMPT_CAPTURE_DIR/after-main-count.txt"
echo "parent after" > "${SAKUSEN_WORKTREE}/parent-after.txt"
