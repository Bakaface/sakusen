# internal/tui — Terminal UI

BubbleTea terminal UI for monitoring and task management. Load `/tui` skill before making substantial changes.

## Critical Invariants

- **`lipgloss.Width()` must match expected frame width for every line** — verify rendering programmatically, never by reasoning alone (see root CLAUDE.md "Verifying Non-Interactive Output")
- **Chord registry in `chords.go`** — new two-key sequences added by appending to `chordRegistry[view]` in `init()`; don't add standalone handlers
- **A parallel group renders as ONE numbered row plus indented branch sub-rows** — in `task_info.go` a group gets a `[parallel K/N, require:<value>]` badge (K/N stays on the row after completion; it is otherwise the only record of how many branches produced output) and one `      <icon> <branch>[ [agent:<slug>]]` sub-row per branch, driven by `TaskInfo.BranchStatus` (○ pending / ● running / ✓ completed / ✗ failed; a missing entry is pending). The current-step marker lands on the GROUP row only, never on a branch. Like the top-level step rows, sub-rows show only EXPLICIT agents (the branch's own or one inherited from the group) — this panel has no agent registry and never re-resolves the default cascade. Step-index arithmetic is unaffected: a group is one entry in `wf.Steps`, so `workflow.HasMoreSteps` and the task-list badge need no change.
- **Step-list selectors stay index-parallel with `m.taskSteps`** — `handleSelectorChoice` recovers the bare step name by indexing `m.taskSteps[cursor]`, so any filtering of the selector items must filter `m.taskSteps` identically. The retry picker does exactly that (`topLevelSteps` drops branch rows, since the daemon rejects retrying a branch); the step-context selector keeps every row and only decorates branches with a `  └ ` prefix via `stepRowLabel`.
- **Worktree toggle state (`alt+w`) persists in DB and session** — affects branch input visibility and `task.Worktree` flag
