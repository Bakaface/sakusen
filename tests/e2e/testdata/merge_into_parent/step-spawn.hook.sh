#!/usr/bin/env bash
# step-spawn.hook.sh — a parent that spawns two children targeting its own
# branch (`--target <parent branch>`, the CLI equivalent of
# create_tasks_and_wait's merge_into_parent) and waits for them.
#
# The first run leaves the parent worktree clean (marker under the ignored
# .sakusen/ dir) so the children's merges into it are not blocked. The resume
# run records what the parent sees: its worktree's files and main's commit
# count, then adds its own file so the parent's merge into main has a diff.

set -euo pipefail

MARKER="${SAKUSEN_WORKTREE}/.sakusen/spawned.marker"
CAPTURE="${E2E_PROMPT_CAPTURE_DIR}"

if [[ -f "$MARKER" ]]; then
    ls "$SAKUSEN_WORKTREE" > "$CAPTURE/resume-files.txt"
    git -C "$SAKUSEN_PROJECT_PATH" rev-list --count main > "$CAPTURE/resume-main-count.txt"
    echo "parent resumed" > "${SAKUSEN_WORKTREE}/parent-resumed.txt"
    exit 0
fi

PARENT_BRANCH="$(git -C "$SAKUSEN_WORKTREE" branch --show-current)"
cd "$SAKUSEN_PROJECT_PATH"

ids=()
for n in one two; do
    out=$("$SAKUSEN_E2E_BIN" create --title "child $n" -w child --target "$PARENT_BRANCH" "child $n task" 2>&1)
    id=$(echo "$out" | grep -oE 'Task #[0-9]+' | head -1 | grep -oE '[0-9]+')
    if [[ -z "$id" ]]; then
        echo "step-spawn.hook.sh: failed to parse child id from: $out" >&2
        exit 1
    fi
    ids+=("$id")
done

"$SAKUSEN_E2E_BIN" wait-for-tasks --use-env "${ids[@]}" >/dev/null
mkdir -p "$(dirname "$MARKER")"
touch "$MARKER"
