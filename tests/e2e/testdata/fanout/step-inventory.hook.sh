#!/usr/bin/env bash
# Publishes two items for the `units` fanout. The items file lives outside the
# worktree so the parent stays a clean merge target for its children.

set -euo pipefail

ITEMS="$(mktemp)"
trap 'rm -f "$ITEMS"' EXIT
cat > "$ITEMS" <<'JSON'
[
  {"slug": "alpha", "title": "unit alpha", "input": "build the alpha unit"},
  {"slug": "beta", "title": "unit beta", "input": "build the beta unit"}
]
JSON

cd "$SAKUSEN_PROJECT_PATH"
"$SAKUSEN_E2E_BIN" publish-items --file "$ITEMS" >/dev/null
