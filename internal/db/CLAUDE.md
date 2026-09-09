# internal/db — SQLite Persistence

SQLite with WAL mode, single writer (`MaxOpenConns=1`), foreign keys on. `schema.sql` is the canonical schema for fresh databases; existing ones are upgraded by the `migrations` ladder in `db.go`.

## Critical Invariants

- **A schema change is a migration AND a `schema.sql` edit in the same change** — append `migrateVN` to the `migrations` ladder (`migrations[0]` upgrades 1 → 2, so `latestSchemaVersion` is derived as `1 + len(migrations)`, never hardcoded) and mirror the change in `schema.sql`; handle NULL defaults for existing rows
- **`ClaimTask(id)` is atomic: pending → running with `started_at`** — returns false if not pending; prevents duplicate execution
- **`started_at` is write-once** — `ClaimTask` sets the real start; `UpdateTaskStatus(StatusRunning)` uses `COALESCE(started_at, ?)` because a task re-enters "running" many times per run (`markSummarizingStep` restores it after every per-step summarization), and an unconditional write reported the duration of the last summarizer round trip instead of the run. The retry paths `NULL` the column to re-arm it
- **`GetClaimableTasks()` filters blocked dependencies, orders by priority desc then `created_at` asc** — ordering matters for fairness
- **Parameterized queries only (`?` placeholders)** — never use string interpolation in SQL
- **`taskColumns` and `scanTaskRow`'s Scan arg list must change in lockstep** (now including `track_id`) — a drift silently mis-scans every task row
- **Track slug uniqueness is per-scope via partial unique indexes** (`idx_tracks_project_slug` / `idx_tracks_global_slug`) — a plain `UNIQUE(project_id, slug)` would NOT cover global tracks because SQLite treats NULLs as distinct
- **Track slugs cannot be empty or purely numeric** — numeric refs resolve as track IDs everywhere (`resolveTrackRef`, `--track`, `--parent`), so a numeric slug would be unreachable; rejected in `CreateTrack`
- **Track parent scope rule + depth cap (10) are enforced in `CreateTrack`** — parents must be same-or-broader scope (project child → global parent OK, never the reverse, never cross-project); creation-time depth check is the only cycle guard because tracks cannot be re-parented
- **Track context append is a single SQL statement** (`context = CASE ... context || char(10) || char(10) || ?`) — no read-modify-write race under concurrent agents

## File Map

| File | Purpose |
|------|---------|
| `db.go` | `DB` wrapper, open/close, `migrations` ladder, version stamping |
| `schema.sql` | Canonical table definitions applied to fresh databases |
| `project.go` | Project upsert/lookup by path, name, id; default worktree flag |
| `task.go` | Task create/claim/query/update/reset/delete, commit tracking, dependency edges (`task_dependencies`, BFS cycle check) |
| `task_steps.go` | Per-step results (`task_steps`): create/complete/fail/context read+overwrite/delete |
| `task_waits_on.go` | Parent → child suspension edges (`task_waits_on`) |
| `track.go` | Tracks: create (slug/scope/depth validation), lookup by slug with project-shadows-global, chain, context/description updates |
| `chat.go` | `chats` rows linking task steps to agent chat sessions (used for resume) |
| `periodic.go` | Routine rows (`periodic_definitions`) and their run history; an empty `cadence` marks an on-demand routine, excluded from the due-query and the claim |

## Conventions

- `task_steps.status` has THREE values: `running`, `completed`, and `failed`. `failed` (`FailTaskStep`) is written only for PARALLEL BRANCH rows, where the join has to tell "ran and lost" from "never started". An ordinary step that fails still leaves its row at `running` — retry-from-step and the TUI key off that, and changing it would be a behavior change well beyond parallel groups.
- Reset variants differ in what they keep: `ResetTaskForRetry` clears step index; `ResetTaskForRetryFromStep` keeps it; `ResetTaskForContinue` also swaps workflow and input. All delete `task_steps`.
- Images and commit hashes are JSON arrays in TEXT columns; nullable fields scan through `sql.NullString` / `sql.NullInt64` / `sql.NullTime`.
- `blocked_by` is computed from `task_dependencies`, never stored on the task row.
- Tests open a real file with `Open(filepath.Join(t.TempDir(), "test.db"))`.
