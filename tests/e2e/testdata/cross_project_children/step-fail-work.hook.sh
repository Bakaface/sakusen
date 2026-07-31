#!/usr/bin/env bash
# Deliberate failure: stub-claude.sh runs with `set -euo pipefail` and invokes
# this hook as a bare command, so a non-zero exit aborts the stub. The step then
# exits non-zero and the task lands in "failed" — which is what the parent's
# {{children.<id>.status}} must report on resume.
echo "child $SORTIE_TASK_ID failing" >&2
exit 1
