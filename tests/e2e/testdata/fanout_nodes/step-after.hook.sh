#!/usr/bin/env bash
# Records main's commit count once the fanout joined, then adds its own file
# so the parent's merge into main has a diff.

set -euo pipefail

git -C "$SAKUSEN_PROJECT_PATH" rev-list --count main > "$E2E_PROMPT_CAPTURE_DIR/after-main-count.txt"
echo "parent after" > "${SAKUSEN_WORKTREE}/parent-after.txt"
