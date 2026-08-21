---
name: task-lifecycle
description: >
  Sakusen's task model: status state machine, priority system, dependency blocking,
  title sanitization, and the Task struct. Use when editing files in internal/task/,
  working on task status transitions, priority management, or any code that changes
  task status or creates tasks.
---

# Task Lifecycle

## File Map

| File | Purpose |
|------|---------|
| `task.go` | Task struct, Status/Priority enums, title sanitization, slugification |

## Task Struct

```go
type Task struct {
    ID, ProjectID             int64
    Title, Input, Slug        string
    Workflow                   string
    Status                     Status    // typed string enum
    Priority                   Priority  // typed string enum
    StepIndex                  int
    CurrentStep                string
    LoopIteration              int
    BranchName                 string    // User template (e.g. "feature/{{task.title}}")
    Branch                     string    // Resolved name
    TargetBranch               string    // Per-task override for base/merge branch
    CheckoutBranch             string    // Use an existing branch instead of creating a new one
    Worktree                   bool      // Default true; false = run in project root
    WorktreePath               string
    WorktreeDetached           bool
    ExitCode                   *int
    ErrorMessage, Context      string
    BlockedBy                  []int64
    Images                     []string
    Commits                    []string
    CreatedAt                  time.Time
    StartedAt, CompletedAt     *time.Time
    UpdatedAt                  time.Time
}
```

## Status State Machine

```
init -> pending -> running -+-> summarizing_step -> running (next step)
                            +-> summarizing -> completed
                            +-> awaiting-approval -> running (resumed)
                            +-> tmux -> finalizing -> summarizing -> completed
                            +-> merge-blocked -> completed
                            +-> failed
```

**Title refinement**: During `init`, an async goroutine generates an AI title via the configured `summarizer:` command (`SAKUSEN_PURPOSE=title`, 30s timeout; with no summarizer, a sanitized truncated input is used). On success, `FinalizeTaskIdentity()` updates title/slug/branch before transitioning to `pending`. On failure, the sanitized input is kept as title.

**Terminal:** `completed`, `failed`
**Active:** `running`, `awaiting-approval`, `tmux`, `finalizing`, `summarizing`, `summarizing_step`, `merge-blocked`

```go
func (s Status) IsTerminal() bool  // completed, failed
func (s Status) IsActive() bool    // running, awaiting-approval, tmux, finalizing, summarizing, summarizing_step, merge-blocked
```

`StatusSummarizingStep` is a transient between-step state used when the workflow engine is running `summarize_chat` against the just-completed step before advancing to the next one.

## Priority

| Priority | Value | Sort |
|----------|-------|------|
| urgent   | 4     | First |
| high     | 3     | Second |
| medium   | 2     | Default |
| low      | 1     | Last |

```go
func (p Priority) Value() int          // Returns numeric value (1-4, default 2)
func ValidPriorities() []Priority      // [low, medium, high, urgent]
func IsValidPriority(s string) bool    // Checks against valid list
```

`GetClaimableTasks()` orders by priority desc, then `created_at` asc.

## Title Handling

- `SanitizeTitle()`: first line, collapse whitespace, strip control chars, max 80 chars (`MaxTitleLength`)
- `Slugify()`: lowercase, non-alphanumeric -> hyphens, trim, max 40 chars

## Patterns

- Status transitions via `db.UpdateTaskStatus()`, not direct field assignment
- `Worktree` defaults to `true`; when `false`, task runs in project root
- `Context` stores summarizer output after workflow completion
- `Images` is `[]string` stored as JSON in the database
- `BlockedBy` computed from `task_dependencies` table at query time
