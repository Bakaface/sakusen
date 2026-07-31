#!/usr/bin/env bash
# step-spawn.hook.sh — simulates an agent that fans out into ANOTHER project.
#
# First invocation (parent's initial run of the spawn step):
#   1. Read the child project path and the child workflow specs from files the
#      test dropped in $HOME (see Env.WriteHookFile — HOME is the per-test
#      XDG dir and the daemon passes os.Environ() to the claude process, so
#      this is the only channel that reaches an already-started daemon).
#   2. cd into the CHILD project (sortie resolves the project from cwd) and
#      create one child task per spec line.
#   3. Register waits-on edges via `sortie wait-for-tasks --use-env`.
#   4. Touch a marker so subsequent invocations skip the spawn.
#
# Second invocation (parent's resume run after all children are terminal):
#   The marker exists, so the hook only writes a file — the resume run still
#   needs a worktree diff for `on_complete: commit`.
#
# The marker gate is ESSENTIAL: without it, the resume run would spawn more
# children and the parent would never escape awaiting-children.

set -euo pipefail

MARKER="${SORTIE_WORKTREE:-/tmp}/.sortie-test-spawned-${SORTIE_TASK_ID}.marker"

if [[ -f "$MARKER" ]]; then
    echo "resumed" >> "${SORTIE_WORKTREE}/resumed.txt"
    exit 0
fi

# The e2e harness exports the binary path via SORTIE_E2E_BIN; the binary lives
# in a one-off tmp build dir and is not on PATH.
SORTIE_BIN="${SORTIE_E2E_BIN:?SORTIE_E2E_BIN not set}"

CHILD_PROJECT="$(cat "${HOME}/e2e-child-project")"
cd "$CHILD_PROJECT"

IDS=()
n=0
while IFS= read -r wf; do
    [[ -z "$wf" ]] && continue
    n=$((n + 1))
    OUT=$("$SORTIE_BIN" create --title "cross child $n" -w "$wf" "cross child $n work" 2>&1)
    ID=$(echo "$OUT" | grep -oE 'Task #[0-9]+' | head -1 | grep -oE '[0-9]+')
    if [[ -z "$ID" ]]; then
        echo "step-spawn.hook.sh: cannot parse child id from: $OUT" >&2
        exit 1
    fi
    IDS+=("$ID")
done < "${HOME}/e2e-child-specs"

if [[ ${#IDS[@]} -eq 0 ]]; then
    echo "step-spawn.hook.sh: no children spawned (empty ${HOME}/e2e-child-specs?)" >&2
    exit 1
fi

# SORTIE_TASK_ID is set by the engine for the spawn step's subprocess.
"$SORTIE_BIN" wait-for-tasks --use-env "${IDS[@]}" >/dev/null

touch "$MARKER"
echo "spawned" >> "${SORTIE_WORKTREE}/spawned.txt"
