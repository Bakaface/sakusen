# internal/agent — Agent State Management

Agent state machine and concurrent agent manager. Load `/claude-process` skill before making
substantial changes (also covers `internal/runner/`).

## Critical Invariants

- **Manager enforces `maxConcurrent` limit** — excess agents queued, not dropped.
- **State transitions route through Manager methods**, never direct field assignment.
- **OnStateChange callback fires outside the mutex** (deadlock prevention).
