#!/usr/bin/env bash
# step-spawn.hook.sh — simulates an agent that calls create_tasks_and_wait.
#
# First invocation (parent's initial run of the spawn step):
#   1. Create two child tasks via `sakusen create -w child`
#   2. Register waits-on edges via `sakusen wait-for-tasks --use-env`
#   3. Touch a marker file so subsequent invocations skip the spawn
#
# Second invocation (parent's resume run after children completed):
#   The marker file exists, so the hook is a no-op. The engine's wait-on
#   probe finds zero edges → step completes normally → parent advances.
#
# This separation (marker file gates the spawn) is essential — without it,
# every step run would create two more children and the parent would never
# escape the awaiting-children state.

set -euo pipefail

MARKER="${SAKUSEN_WORKTREE:-/tmp}/.sakusen-test-spawned-${SAKUSEN_TASK_ID}.marker"

if [[ -f "$MARKER" ]]; then
    # Resume run: do not spawn; just touch a file so the worktree has a
    # diff for finalization.
    echo "resumed" >> "${SAKUSEN_WORKTREE}/resumed.txt"
    exit 0
fi

# Initial run — find the sakusen binary the daemon spawned us from.
# The e2e harness builds the binary and invokes the daemon directly, so the
# binary path is not on PATH. We discover it via the daemon process: the
# common case during tests is that `which sakusen` is empty, but the daemon's
# argv[0] is the absolute path. We fall back to walking up from $SAKUSEN_WORKTREE
# (the tmp build dir doesn't share a tree with the worktree), so we resolve via
# the SAKUSEN binary that the e2e harness exposed through PATH-like wiring.
#
# The e2e harness exports the binary path via SAKUSEN_E2E_BIN if set; otherwise
# we look in well-known locations.
SAKUSEN_BIN="${SAKUSEN_E2E_BIN:-}"
if [[ -z "$SAKUSEN_BIN" ]]; then
    # Daemon's $PATH does not include the sakusen test binary's tmp dir, but
    # the test main_test.go sets sakusenBinPath which is exposed to subprocesses
    # via the test runner's env if we propagate it. Until that is wired, look
    # for "sakusen" on PATH.
    if command -v sakusen >/dev/null 2>&1; then
        SAKUSEN_BIN="$(command -v sakusen)"
    fi
fi
if [[ -z "$SAKUSEN_BIN" ]]; then
    echo "step-spawn.hook.sh: cannot find sakusen binary (set SAKUSEN_E2E_BIN)" >&2
    exit 1
fi

# Spawn two child tasks. The `sakusen create` command resolves the project from
# the current working directory's .sakusen.yml; cd into the parent's project
# root (NOT the worktree) so the children register under the same project.
PROJECT_DIR="${SAKUSEN_PROJECT_PATH:-}"
if [[ -z "$PROJECT_DIR" ]]; then
    echo "step-spawn.hook.sh: SAKUSEN_PROJECT_PATH not set (engine should inject it)" >&2
    exit 1
fi
cd "$PROJECT_DIR"

# Capture child IDs from "Task #N created".
CHILD1_OUT=$("$SAKUSEN_BIN" create --title "child one" -w child "child one task" 2>&1)
CHILD1_ID=$(echo "$CHILD1_OUT" | grep -oE 'Task #[0-9]+' | head -1 | grep -oE '[0-9]+')
if [[ -z "$CHILD1_ID" ]]; then
    echo "step-spawn.hook.sh: failed to parse child1 id from: $CHILD1_OUT" >&2
    exit 1
fi

CHILD2_OUT=$("$SAKUSEN_BIN" create --title "child two" -w child "child two task" 2>&1)
CHILD2_ID=$(echo "$CHILD2_OUT" | grep -oE 'Task #[0-9]+' | head -1 | grep -oE '[0-9]+')
if [[ -z "$CHILD2_ID" ]]; then
    echo "step-spawn.hook.sh: failed to parse child2 id from: $CHILD2_OUT" >&2
    exit 1
fi

# Register waits-on edges. SAKUSEN_TASK_ID is set by the engine for the spawn
# step's subprocess; we use --use-env to pick it up.
"$SAKUSEN_BIN" wait-for-tasks --use-env "$CHILD1_ID" "$CHILD2_ID" >/dev/null

# Mark spawn done so the resume run does NOT spawn again.
touch "$MARKER"
echo "spawned" >> "${SAKUSEN_WORKTREE}/spawned.txt"
