#!/usr/bin/env bash
# Agent command for the result_fallback scenario: deliberately IGNORES
# $SAKUSEN_RESULT_FILE and only prints its result to stdout, exercising
# sakusen's stdout-tail fallback for step context capture.
set -euo pipefail
echo "stdout-only-fallback-line"
