#!/usr/bin/env bash
# Touch a unique file so the worktree has changes for the commit step.
echo "child $SAKUSEN_TASK_ID did work" >> "${SAKUSEN_WORKTREE}/child-$SAKUSEN_TASK_ID-output.txt"
