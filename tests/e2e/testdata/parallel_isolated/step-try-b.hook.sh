#!/usr/bin/env bash
# Writes into the agent's cwd, which must be try-b's own worktree. The sleep
# makes try-b finish last, so require: 1 must still wait for it.
sleep 1
echo "approach B" > try-b.txt
