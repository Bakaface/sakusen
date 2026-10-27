#!/usr/bin/env bash
# Records what node c starts from: its needs' files and commits on the
# parent's branch, and main still untouched.

set -euo pipefail

ls "$SAKUSEN_WORKTREE" > "$E2E_PROMPT_CAPTURE_DIR/c-files.txt"
git -C "$SAKUSEN_WORKTREE" log --format=%s > "$E2E_PROMPT_CAPTURE_DIR/c-log.txt"
git -C "$SAKUSEN_PROJECT_PATH" rev-list --count main > "$E2E_PROMPT_CAPTURE_DIR/c-main-count.txt"
echo "node c did work" > "${SAKUSEN_WORKTREE}/node-c.txt"
