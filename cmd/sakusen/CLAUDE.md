# cmd/sakusen — CLI Entry Points

Cobra-based CLI. Load `/cli` skill before making substantial changes.

## Critical Invariants

- **Config required for non-exempt commands** — `PersistentPreRunE` enforces `.sakusen.yml` exists; only daemon subcommands (`start`, `stop`, `status`), `tui`, and completion are exempt via `noProjectRequired` map
- **Task IDs are `int64`** — parsed from positional args, not strings
- **`--no-worktree` defaults to false** — tasks get isolated worktrees by default
