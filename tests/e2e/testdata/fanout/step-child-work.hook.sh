#!/usr/bin/env bash
# Each child adds its own file, so sibling merges rebase without conflicts.
echo "child $SAKUSEN_TASK_ID did work" > "${SAKUSEN_WORKTREE}/child-$SAKUSEN_TASK_ID.txt"
