#!/usr/bin/env bash
# stub-claude.sh — fake Claude binary for e2e tests.
#
# Routing key priority:
#   1. $E2E_STUB_OVERRIDE — if set and executable, exec it with the same args.
#   2. $SORTIE_PURPOSE    — primary routing key (title, summarize, summarize_chat,
#                           merge_conflict, step).
#   3. cwd path fallback  — for tmux-launched steps where SORTIE_PURPOSE may not
#                           be set; looks for .sortie/worktrees/<branch>/ in cwd.
#   4. default.ndjson / default.txt
#
# For "step" purpose, the step name is taken from $SORTIE_STEP (set by the
# workflow engine before each step). This is more reliable than parsing the
# --system-prompt argument.
#
# Files are looked up under $E2E_RESPONSES_DIR.
# If $E2E_RESPONSES_DIR/.current-subdir exists, its content is appended as a
# subdirectory (enables SwapResponses without restarting the daemon).
#
# Side effects every call:
#   Appends one tab-separated line to $SORTIE_E2E_LOG:
#   <RFC3339Nano>\t<purpose>\t<cwd>\t<step>\t<env-pairs|…>
#   (argv is omitted — it contains the system prompt which has newlines.)
#
# Non-step purposes emit plain text; step purposes emit NDJSON stream-json.
# The --output-format flag is checked; if stream-json the .ndjson file is emitted,
# otherwise the .txt equivalent or .ndjson as fallback.

set -euo pipefail

TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%S.%NZ" 2>/dev/null || date -u +"%Y-%m-%dT%H:%M:%SZ")
PURPOSE="${SORTIE_PURPOSE:-}"
CWD="$(pwd)"

# --- override ---
if [[ -n "${E2E_STUB_OVERRIDE:-}" && -x "$E2E_STUB_OVERRIDE" ]]; then
    exec "$E2E_STUB_OVERRIDE" "$@"
fi

# --- resolve responses dir ---
RDIR="${E2E_RESPONSES_DIR:-}"
if [[ -n "$RDIR" && -f "$RDIR/.current-subdir" ]]; then
    SUBDIR="$(cat "$RDIR/.current-subdir")"
    if [[ -n "$SUBDIR" ]]; then
        RDIR="$RDIR/$SUBDIR"
    fi
fi

# --- detect output format ---
OUTPUT_FORMAT="text"
for arg in "$@"; do
    if [[ "$arg" == "--output-format=stream-json" ]]; then
        OUTPUT_FORMAT="stream-json"
        break
    fi
done
# Also handle space-separated: --output-format stream-json
prev=""
for arg in "$@"; do
    if [[ "$prev" == "--output-format" ]]; then
        OUTPUT_FORMAT="$arg"
        break
    fi
    prev="$arg"
done

# --- step name from env (set by sortie's workflow engine per step) ---
STEP_NAME="${SORTIE_STEP:-}"

# --- cwd fallback: tmux-launched steps may have no SORTIE_PURPOSE ---
if [[ -z "$PURPOSE" && "$CWD" =~ /worktrees/[^/]+ ]]; then
    PURPOSE="step"
fi

# --- log invocation (TSV: timestamp, purpose, cwd, step, sortie-env-pairs) ---
if [[ -n "${SORTIE_E2E_LOG:-}" ]]; then
    ENV_PAIRS=""
    for var in $(env | grep -E '^SORTIE_' | cut -d= -f1); do
        val="${!var:-}"
        # Strip any newlines from values to keep the TSV line intact.
        val="${val//$'\n'/ }"
        ENV_PAIRS="${ENV_PAIRS}${var}=${val}|"
    done
    printf '%s\t%s\t%s\t%s\t%s\n' \
        "$TIMESTAMP" "$PURPOSE" "$CWD" "$STEP_NAME" "${ENV_PAIRS%|}" \
        >> "$SORTIE_E2E_LOG"
fi

# --- opt-in prompt capture (the TSV log deliberately omits argv because it
# contains multi-line prompts; tests that need to assert on prompt contents
# set E2E_PROMPT_CAPTURE_DIR and read prompt-<taskID>-<step>.txt) ---
if [[ -n "${E2E_PROMPT_CAPTURE_DIR:-}" ]]; then
    printf '%s\n' "$*" > "$E2E_PROMPT_CAPTURE_DIR/prompt-${SORTIE_TASK_ID:-0}-${STEP_NAME:-none}.txt"
fi

# --- route to response file ---
emit_file() {
    local path="$1"
    if [[ ! -f "$path" ]]; then
        echo "stub-claude.sh: response file not found: $path" >&2
        exit 1
    fi
    cat "$path"
}

case "$PURPOSE" in
    title)
        emit_file "${RDIR}/title.txt"
        exit 0
        ;;
    summarize)
        emit_file "${RDIR}/summarize.txt"
        exit 0
        ;;
    summarize_chat)
        emit_file "${RDIR}/summarize_chat.txt"
        exit 0
        ;;
    merge_conflict)
        emit_file "${RDIR}/merge_conflict.ndjson"
        exit 0
        ;;
    step|"")
        # Determine step file name
        STEP_FILE=""
        if [[ -n "$STEP_NAME" ]]; then
            STEP_FILE="${RDIR}/step-${STEP_NAME}.ndjson"
        fi
        if [[ -z "$STEP_FILE" || ! -f "$STEP_FILE" ]]; then
            STEP_FILE="${RDIR}/default.ndjson"
        fi

        # Run hook if present
        HOOK=""
        if [[ -n "$STEP_NAME" ]]; then
            HOOK="${RDIR}/step-${STEP_NAME}.hook.sh"
        fi
        if [[ -n "$HOOK" && -f "$HOOK" && -x "$HOOK" ]]; then
            "$HOOK"
        fi

        emit_file "$STEP_FILE"
        exit 0
        ;;
    *)
        # Unknown purpose: try default
        if [[ -f "${RDIR}/default.ndjson" ]]; then
            emit_file "${RDIR}/default.ndjson"
        elif [[ -f "${RDIR}/default.txt" ]]; then
            emit_file "${RDIR}/default.txt"
        else
            echo "stub-claude.sh: no response for purpose=${PURPOSE}" >&2
            exit 1
        fi
        exit 0
        ;;
esac
