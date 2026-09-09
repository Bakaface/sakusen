#!/usr/bin/env bash
# Fails the review-b branch so the group's join policy is exercised:
# require:any advances the task, require:all fails it.
echo "review-b hook: simulated branch failure" >&2
exit 1
