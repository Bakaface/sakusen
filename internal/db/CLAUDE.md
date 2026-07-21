# internal/db — SQLite Persistence

Schema, migrations, task/project queries. Load `/database` skill before making substantial changes.

## Critical Invariants

- **`ClaimTask(id)` is atomic: pending → running with `started_at`** — returns false if not pending; prevents duplicate execution
- **`GetClaimableTasks()` filters blocked dependencies, orders by priority desc then `created_at` asc** — ordering matters for fairness
- **Parameterized queries only (`?` placeholders)** — never use string interpolation in SQL
- **`taskColumns` and `scanTaskRow`'s Scan arg list must change in lockstep** (now including `track_id`) — a drift silently mis-scans every task row
- **Track slug uniqueness is per-scope via partial unique indexes** (`idx_tracks_project_slug` / `idx_tracks_global_slug`) — a plain `UNIQUE(project_id, slug)` would NOT cover global tracks because SQLite treats NULLs as distinct
- **Track slugs cannot be empty or purely numeric** — numeric refs resolve as track IDs everywhere (`resolveTrackRef`, `--track`, `--parent`), so a numeric slug would be unreachable; rejected in `CreateTrack`
- **Track parent scope rule + depth cap (10) are enforced in `CreateTrack`** — parents must be same-or-broader scope (project child → global parent OK, never the reverse, never cross-project); creation-time depth check is the only cycle guard because tracks cannot be re-parented
- **Track context append is a single SQL statement** (`context = CASE ... context || char(10) || char(10) || ?`) — no read-modify-write race under concurrent agents
