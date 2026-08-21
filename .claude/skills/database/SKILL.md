---
name: database
description: >
  Sakusen's SQLite persistence layer: schema, migrations, task/project queries, and
  dependency management. Use when editing files in internal/db/, working on schema
  migrations, task queries, project persistence, or dependency blocking logic.
---

# Database & Persistence

SQLite with WAL mode, single writer (`MaxOpenConns=1`), foreign keys enabled. Schema versioned with progressive migrations (currently **v21**).

## Schema

Read `internal/db/schema.sql` for the canonical table definitions. Core tables: `projects`, `tasks`, `task_dependencies`, `task_steps`, `task_waits_on`, `tracks`, plus `chats` for conversation tracking. Migrations live in the `migrations` ladder in `db.go` (`migrations[0]` upgrades version 1 → 2, so the last migration's target version is `len(migrations)+1`); auto-applied on startup. Fresh databases apply the embedded `schema.sql` directly and stamp `latestSchemaVersion` (derived as `1 + len(migrations)` — never hardcoded). When adding a migration, append `migrateVN` to the ladder AND mirror the change in `schema.sql` in the same change.

### `task_steps` Table

Stores per-step execution results for each task run. Populated by the workflow engine after each step completes.

| Column | Type | Description |
|--------|------|-------------|
| `id` | INTEGER PK | Auto-increment |
| `task_id` | INTEGER FK | References `tasks.id` |
| `step_name` | TEXT | Step identifier (matches workflow step `name`) |
| `status` | TEXT | Step execution status |
| `context` | TEXT | Step result captured from Claude's NDJSON `result` event |
| `exit_code` | INTEGER | Claude process exit code |
| `started_at` | DATETIME | When step execution began |
| `completed_at` | DATETIME | When step execution finished |

Step contexts are fetched via daemon RPC for TUI display. Template access: `{{steps.<step_name>.context}}` (or backward-compat `{{artifacts.<step_name>}}`).

## Project Operations

```go
type Project struct {
    ID              int64
    Path            string
    Name            string
    DefaultPriority task.Priority
    DefaultWorktree bool
    CreatedAt       time.Time
}

GetOrCreateProject(projectPath string) (*Project, error)  // Upsert by path
GetProjectByPath(path string) (*Project, error)
GetProject(id int64) (*Project, error)
GetProjectsByName(name string) ([]*Project, error)
ListProjects() ([]*Project, error)
UpdateProjectDefaultWorktree(id int64, worktree bool) error
```

## Task Creation

```go
CreateTask(projectID int64, title, input, slug, workflow, branch string, status task.Status, images []string) (*task.Task, error)
CreateTaskWithPriority(projectID int64, title, input, slug, workflow, branchName, branch, targetBranch, checkoutBranch string, status task.Status, priority task.Priority, worktree bool, images []string, trackID *int64) (*task.Task, error)
```

`CreateTask` is a convenience wrapper that delegates to `CreateTaskWithPriority` with medium priority, `worktree=true`, and no track.

## Task Query Patterns

### Status-Filtered Queries
- `GetPendingTasks()` / `GetRunningTasks()` — filter by status
- `GetClaimableTasks()` — pending tasks not blocked by incomplete dependencies, ordered by priority desc then created_at asc
- `GetAllTasks()` — all tasks regardless of status
- `GetTasksByProject(projectID int64)` — tasks for a specific project
- `GetTasksByProjectName(name string)` — tasks by project name

### ClaimTask(id)
Atomically transition pending -> running with `started_at`. Returns `(bool, error)` — false if not pending.

### Field Update Functions
```go
UpdateTaskStatus(id int64, status task.Status) error
UpdateTaskWorktreePath(id int64, worktreePath string) error
UpdateTaskBranch(id int64, branch string) error
ClearWorktreePath(id int64) error
UpdateTaskStep(id int64, stepIndex int, currentStep string) error
UpdateTaskExitCode(id int64, exitCode int, errorMessage string) error
UpdateTaskError(id int64, errMsg string) error
UpdateTaskPriority(id int64, priority task.Priority) error
UpdateTaskContext(id int64, taskContext string) error
UpdateTaskTitle(id int64, title string) error
UpdateTaskInput(id int64, input string) error
FinalizeTaskIdentity(id int64, title, slug, branch string) error
UpdateTaskLoopIteration(id int64, iteration int) error
SetWorktreeDetached(id int64, detached bool) error
```

### Commit Tracking
```go
AppendTaskCommit(id int64, commitHash string) error  // Append to JSON array of commit hashes
GetTaskCommits(id int64) ([]string, error)            // Read commit hashes from JSON array
```

### Reset Operations
- `ResetTaskForRetry(id int64)` — reset to pending, clear step/error/timing, delete task_steps via `DeleteTaskSteps()`
- `ResetTaskForRetryFromStep(id int64)` — reset to pending, clear current_step/error but **keep step_index**, delete task_steps via `DeleteTaskSteps()`
- `ResetTaskForContinue(id int64, workflow, prompt string)` — reset to pending, update workflow and input prompt, delete task_steps via `DeleteTaskSteps()`
- `DeleteTask(id int64)` — hard delete (also removes task_dependencies)

### Dependency Management
```go
AddTaskDependency(taskID, blockedByID int64) error                // INSERT OR IGNORE
RemoveTaskDependency(taskID, blockedByID int64) error             // Delete single edge
SetTaskDependencies(taskID int64, blockedBy []int64) error        // Replace all deps in a transaction
HasCircularDependency(taskID, newBlockedByID int64) (bool, error) // BFS cycle detection
```

### Task Step Operations
```go
CreateTaskStep(taskID int64, stepName string) error                               // INSERT OR REPLACE with status='running'
CompleteTaskStep(taskID int64, stepName string, context *string, exitCode int) error // Update to 'completed' with context/exit_code
UpdateTaskStepContext(taskID int64, stepName string, context string) error          // Overwrite context for a completed step (used by background summarize_chat)
GetTaskStepContext(taskID int64, stepName string) (string, error)                  // Single completed step context
GetTaskStepContexts(taskID int64, stepNames []string) (map[string]string, error)  // Multiple step contexts by name
GetAllTaskStepContexts(taskID int64) (map[string]string, error)                   // All completed step contexts
DeleteTaskSteps(taskID int64) error                                               // Delete all steps for a task
DeleteTaskStepsFrom(taskID int64, stepNames []string) error                       // Delete specific steps by name
```

### Track Operations (`track.go`)

Tracks are named, mutable, hierarchical context containers (`tracks` table; see `internal/task/track.go`). `project_id` NULL = global track; slug uniqueness is per-scope via two partial unique indexes. Tasks reference a track via `tasks.track_id`.

```go
CreateTrack(projectID *int64, name, slug, workflow, context, description string, parentID *int64) (*task.Track, error) // validates slug (non-empty, not purely numeric), parent scope, depth cap (maxTrackDepth=10)
GetTrack(id int64) (*task.Track, error)
GetTrackBySlug(projectID *int64, slug string) (*task.Track, error) // project shadows global; nil projectID = global only
ListTracks(projectID int64) ([]*task.Track, error)                 // project + global tracks, project first, slug-ordered
UpdateTrackContext(id int64, context, mode string) error           // mode "replace" (default) or "append" — append is a single SQL statement (race-free)
UpdateTrackDescription(id int64, description string) error         // replace-only; the stable purpose one-liner, distinct from the context accumulator
GetTrackChain(id int64) ([]*task.Track, error)                     // track + ancestors, root-first; in-Go loop, errors past maxTrackDepth
```

## Patterns

- Parameterized queries only (`?` placeholders), never string interpolation
- Images stored as JSON array: `json.Marshal`/`json.Unmarshal`
- Nullable fields use `sql.NullString`, `sql.NullInt64`, `sql.NullTime`
- `blocked_by` computed from `task_dependencies` table, not stored directly
- Test with `Open(filepath.Join(t.TempDir(), "test.db"))` using a temp directory
- New columns: append a migration to the `migrations` ladder (and mirror in `schema.sql`), handle NULL defaults for existing rows
