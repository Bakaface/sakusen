#!/usr/bin/env bash
# Touch a unique file so the worktree has changes for the commit step.
echo "child $SORTIE_TASK_ID did work" >> "${SORTIE_WORKTREE}/child-$SORTIE_TASK_ID-output.txt"
