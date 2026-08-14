#!/usr/bin/env bash
# stub-agent.sh — generic stub agent for e2e tests.
#
# Wired into test .sortie.yml files as a headless agent command AND as the
# summarizer command. Sortie's env contract drives everything — no argv:
#
#   $SORTIE_PURPOSE      routing key: "step" or "merge_conflict" (agent spawns,
#                        routed via the $SORTIE_RESULT_FILE contract) or a
#                        summarizer purpose (title, summarize, summarize_chat,
#                        summarize_chat_chunk, backfill_context).
#   $SORTIE_STEP         step name for step spawns.
#   $SORTIE_PROMPT_FILE  resolved step prompt (step spawns).
#   $SORTIE_RESULT_FILE  where the final result text must be written (step spawns).
#
# Response files are looked up under $E2E_RESPONSES_DIR:
#   step spawns       → step-<step>.txt, else default.txt   (content becomes the
#                       step's result text, written to $SORTIE_RESULT_FILE;
#                       stdout gets a DECOY line instead so tests prove that
#                       production reads the result file, not the stdout tail)
#   other purposes    → <purpose>.txt (e.g. title.txt, summarize.txt)
# If $E2E_RESPONSES_DIR/.current-subdir exists, its content is appended as a
# subdirectory (enables SwapResponses without restarting the daemon).
#
# A per-step hook step-<step>.hook.sh runs (if executable) before the response
# is emitted — used to simulate agents that touch files or call sortie CLI.
#
# Side effects every call:
#   Appends one tab-separated line to $SORTIE_E2E_LOG:
#   <RFC3339Nano>\t<purpose>\t<cwd>\t<step>\t<env-pairs|…>
#   (env-pairs covers SORTIE_* and E2E_* vars, so tests can assert per-agent
#   `env:` injection via markers like E2E_AGENT_MARKER.)
#   Captures the step prompt to $E2E_PROMPT_CAPTURE_DIR/prompt-<task>-<step>.txt.

set -euo pipefail

TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%S.%NZ" 2>/dev/null || date -u +"%Y-%m-%dT%H:%M:%SZ")
PURPOSE="${SORTIE_PURPOSE:-step}"
STEP_NAME="${SORTIE_STEP:-}"
CWD="$(pwd)"

# --- resolve responses dir ---
RDIR="${E2E_RESPONSES_DIR:-}"
if [[ -n "$RDIR" && -f "$RDIR/.current-subdir" ]]; then
    SUBDIR="$(cat "$RDIR/.current-subdir")"
    if [[ -n "$SUBDIR" ]]; then
        RDIR="$RDIR/$SUBDIR"
    fi
fi

# --- log invocation (TSV: timestamp, purpose, cwd, step, sortie-env-pairs) ---
if [[ -n "${SORTIE_E2E_LOG:-}" ]]; then
    ENV_PAIRS=""
    for var in $(env | grep -E '^(SORTIE|E2E)_' | cut -d= -f1); do
        val="${!var:-}"
        # Strip any newlines from values to keep the TSV line intact.
        val="${val//$'\n'/ }"
        ENV_PAIRS="${ENV_PAIRS}${var}=${val}|"
    done
    printf '%s\t%s\t%s\t%s\t%s\n' \
        "$TIMESTAMP" "$PURPOSE" "$CWD" "$STEP_NAME" "${ENV_PAIRS%|}" \
        >> "$SORTIE_E2E_LOG"
fi

# --- opt-in prompt capture (step prompts arrive via $SORTIE_PROMPT_FILE) ---
if [[ -n "${E2E_PROMPT_CAPTURE_DIR:-}" && -n "${SORTIE_PROMPT_FILE:-}" && -f "${SORTIE_PROMPT_FILE:-}" ]]; then
    cp "$SORTIE_PROMPT_FILE" "$E2E_PROMPT_CAPTURE_DIR/prompt-${SORTIE_TASK_ID:-0}-${STEP_NAME:-none}.txt"
fi

emit_file() {
    local path="$1"
    if [[ ! -f "$path" ]]; then
        echo "stub-agent.sh: response file not found: $path" >&2
        exit 1
    fi
    cat "$path"
}

case "$PURPOSE" in
    step|merge_conflict)
        # Determine step response file
        STEP_FILE=""
        if [[ -n "$STEP_NAME" ]]; then
            STEP_FILE="${RDIR}/step-${STEP_NAME}.txt"
        fi
        if [[ -z "$STEP_FILE" || ! -f "$STEP_FILE" ]]; then
            STEP_FILE="${RDIR}/default.txt"
        fi

        # Run hook if present
        HOOK=""
        if [[ -n "$STEP_NAME" ]]; then
            HOOK="${RDIR}/step-${STEP_NAME}.hook.sh"
        fi
        if [[ -n "$HOOK" && -f "$HOOK" && -x "$HOOK" ]]; then
            "$HOOK"
        fi

        # Result text goes into $SORTIE_RESULT_FILE. When the result file is
        # written, stdout deliberately carries a DECOY line instead of the
        # response text: if production ever stopped reading the result file
        # and fell back to the stdout tail, every step-context assertion in
        # the suite would see the decoy and fail. The response goes to stdout
        # ONLY when $SORTIE_RESULT_FILE is unset.
        if [[ -n "${SORTIE_RESULT_FILE:-}" ]]; then
            emit_file "$STEP_FILE" > "$SORTIE_RESULT_FILE"
            echo "stub-stdout-decoy"
        else
            emit_file "$STEP_FILE"
        fi
        exit 0
        ;;
    *)
        # Summarizer-style call: prompt on stdin (consume it), response on stdout.
        cat > /dev/null || true
        if [[ -f "${RDIR}/${PURPOSE}.txt" ]]; then
            emit_file "${RDIR}/${PURPOSE}.txt"
        elif [[ -f "${RDIR}/default.txt" ]]; then
            emit_file "${RDIR}/default.txt"
        else
            echo "stub-agent.sh: no response for purpose=${PURPOSE}" >&2
            exit 1
        fi
        exit 0
        ;;
esac
