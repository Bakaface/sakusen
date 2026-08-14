#!/usr/bin/env bash
# Agent command for the result_fallback scenario: deliberately IGNORES
# $SORTIE_RESULT_FILE and only prints its result to stdout, exercising
# sortie's stdout-tail fallback for step context capture.
set -euo pipefail
echo "stdout-only-fallback-line"
