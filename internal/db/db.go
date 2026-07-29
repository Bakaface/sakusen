package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// DB wraps a *sql.DB. The connection is held in an unexported field rather
// than embedded so the raw Query/Exec/QueryRow/Begin surface is NOT promoted
// onto DB — every query must go through a named method on this type. That is
// the enforcement mechanism for the "no SQL outside internal/db" convention;
// see internal/db/CLAUDE.md.
type DB struct {
	sqlDB *sql.DB
	path  string
}

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	sqlDB, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// SQLite is single-writer; limit connections to prevent contention
	sqlDB.SetMaxOpenConns(1)

	db := &DB{sqlDB: sqlDB, path: path}

	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return db, nil
}

// Close closes the underlying database connection.
func (db *DB) Close() error {
	return db.sqlDB.Close()
}

// migrations is the upgrade ladder, one func per schema version starting at
// 2 (index 0 brings a pre-migration-system database, implicitly version 1,
// up to version 2). The terminal schema version is derived from its length
// (see latestSchemaVersion) instead of being hardcoded a second time — before
// this, a fresh install's `INSERT ... VALUES (19)` and the last `if version <
// 19` block both encoded "19", and adding a migration meant remembering to
// update both.
//
// Each func performs only the schema/data change for its version; migrate()
// wraps the call with the version < N gate and the schema_version bookkeeping
// so individual migrations don't repeat that boilerplate.
var migrations = []func(db *DB) error{
	migrateV2,
	migrateV3,
	migrateV4,
	migrateV5,
	migrateV6,
	migrateV7,
	migrateV8,
	migrateV9,
	migrateV10,
	migrateV11,
	migrateV12,
	migrateV13,
	migrateV14,
	migrateV15,
	migrateV16,
	migrateV17,
	migrateV18,
	migrateV19,
	migrateV20,
	migrateV21,
	migrateV22,
}

// latestSchemaVersion is the schema version a fresh install lands on after
// applying schema.sql directly (schema.sql already reflects every migration
// below). migrations[0] upgrades the implicit version 1 to version 2, so the
// last migration's target version is len(migrations)+1.
var latestSchemaVersion = 1 + len(migrations)

func (db *DB) migrate() error {
	// Create schema version tracking
	_, err := db.sqlDB.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`)
	if err != nil {
		return fmt.Errorf("failed to create schema_version table: %w", err)
	}

	var version int
	row := db.sqlDB.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`)
	if err := row.Scan(&version); err != nil {
		return fmt.Errorf("failed to read schema version: %w", err)
	}

	if version == 0 {
		// Fresh database — apply full schema
		if _, err := db.sqlDB.Exec(schema); err != nil {
			return fmt.Errorf("failed to apply schema: %w", err)
		}
		if _, err := db.sqlDB.Exec(`INSERT INTO schema_version (version) VALUES (?)`, latestSchemaVersion); err != nil {
			return fmt.Errorf("failed to set schema version: %w", err)
		}
		return nil
	}

	for i, m := range migrations {
		target := i + 2
		if version >= target {
			continue
		}
		if err := m(db); err != nil {
			return err
		}
		if _, err := db.sqlDB.Exec(`UPDATE schema_version SET version = ?`, target); err != nil {
			return fmt.Errorf("failed to set schema version: %w", err)
		}
	}

	return nil
}

// migrateV2 adds the workflow column to tasks.
func migrateV2(db *DB) error {
	if _, err := db.sqlDB.Exec(`ALTER TABLE tasks ADD COLUMN workflow TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("failed to add workflow column: %w", err)
	}
	return nil
}

// migrateV3 adds the context column to tasks.
func migrateV3(db *DB) error {
	if _, err := db.sqlDB.Exec(`ALTER TABLE tasks ADD COLUMN context TEXT`); err != nil {
		return fmt.Errorf("failed to add context column: %w", err)
	}
	return nil
}

// migrateV4 renames status values.
func migrateV4(db *DB) error {
	if _, err := db.sqlDB.Exec(`UPDATE tasks SET status = 'init' WHERE status = 'generating-title'`); err != nil {
		return fmt.Errorf("failed to rename generating-title status: %w", err)
	}
	if _, err := db.sqlDB.Exec(`UPDATE tasks SET status = 'awaiting-approval' WHERE status = 'awaiting_approval'`); err != nil {
		return fmt.Errorf("failed to rename awaiting_approval status: %w", err)
	}
	return nil
}

// migrateV5 adds the projects table and project_id to tasks.
func migrateV5(db *DB) error {
	if _, err := db.sqlDB.Exec(`CREATE TABLE IF NOT EXISTS projects (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		path TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("failed to create projects table: %w", err)
	}

	// Add project_id column (nullable initially for migration)
	if _, err := db.sqlDB.Exec(`ALTER TABLE tasks ADD COLUMN project_id INTEGER REFERENCES projects(id)`); err != nil {
		return fmt.Errorf("failed to add project_id column: %w", err)
	}

	// Create a default project for existing tasks
	if _, err := db.sqlDB.Exec(`INSERT OR IGNORE INTO projects (path, name) VALUES ('unknown', 'unknown')`); err != nil {
		return fmt.Errorf("failed to create default project: %w", err)
	}

	// Assign all existing tasks to the default project
	if _, err := db.sqlDB.Exec(`UPDATE tasks SET project_id = (SELECT id FROM projects WHERE path = 'unknown') WHERE project_id IS NULL`); err != nil {
		return fmt.Errorf("failed to assign tasks to default project: %w", err)
	}

	if _, err := db.sqlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_project_id ON tasks(project_id)`); err != nil {
		return fmt.Errorf("failed to create project_id index: %w", err)
	}

	return nil
}

// migrateV6 adds the images column to tasks.
func migrateV6(db *DB) error {
	if _, err := db.sqlDB.Exec(`ALTER TABLE tasks ADD COLUMN images TEXT`); err != nil {
		return fmt.Errorf("failed to add images column: %w", err)
	}
	return nil
}

// migrateV7 adds priority to tasks and default_priority to projects.
func migrateV7(db *DB) error {
	if _, err := db.sqlDB.Exec(`ALTER TABLE tasks ADD COLUMN priority TEXT NOT NULL DEFAULT 'medium'`); err != nil {
		return fmt.Errorf("failed to add priority column to tasks: %w", err)
	}
	if _, err := db.sqlDB.Exec(`ALTER TABLE projects ADD COLUMN default_priority TEXT NOT NULL DEFAULT 'medium'`); err != nil {
		return fmt.Errorf("failed to add default_priority column to projects: %w", err)
	}
	return nil
}

// migrateV8 adds loop_iteration to tasks.
func migrateV8(db *DB) error {
	if _, err := db.sqlDB.Exec(`ALTER TABLE tasks ADD COLUMN loop_iteration INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("failed to add loop_iteration column: %w", err)
	}
	return nil
}

// migrateV9 adds branch_name to tasks (user-provided branch template).
func migrateV9(db *DB) error {
	if _, err := db.sqlDB.Exec(`ALTER TABLE tasks ADD COLUMN branch_name TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("failed to add branch_name column: %w", err)
	}
	return nil
}

// migrateV10 adds the worktree boolean to tasks.
func migrateV10(db *DB) error {
	if _, err := db.sqlDB.Exec(`ALTER TABLE tasks ADD COLUMN worktree INTEGER NOT NULL DEFAULT 1`); err != nil {
		return fmt.Errorf("failed to add worktree column: %w", err)
	}
	return nil
}

// migrateV11 adds default_worktree to projects.
func migrateV11(db *DB) error {
	if _, err := db.sqlDB.Exec(`ALTER TABLE projects ADD COLUMN default_worktree INTEGER NOT NULL DEFAULT 1`); err != nil {
		return fmt.Errorf("failed to add default_worktree column to projects: %w", err)
	}
	return nil
}

// migrateV12 adds the commits column to tasks.
func migrateV12(db *DB) error {
	if _, err := db.sqlDB.Exec(`ALTER TABLE tasks ADD COLUMN commits TEXT`); err != nil {
		return fmt.Errorf("failed to add commits column: %w", err)
	}
	return nil
}

// migrateV13 adds the task_dependencies table.
func migrateV13(db *DB) error {
	if _, err := db.sqlDB.Exec(`CREATE TABLE IF NOT EXISTS task_dependencies (
		task_id INTEGER NOT NULL REFERENCES tasks(id),
		blocked_by INTEGER NOT NULL REFERENCES tasks(id),
		PRIMARY KEY (task_id, blocked_by)
	)`); err != nil {
		return fmt.Errorf("failed to create task_dependencies table: %w", err)
	}
	return nil
}

// migrateV14 adds target_branch and checkout_branch to tasks.
func migrateV14(db *DB) error {
	if _, err := db.sqlDB.Exec(`ALTER TABLE tasks ADD COLUMN target_branch TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("failed to add target_branch column: %w", err)
	}
	if _, err := db.sqlDB.Exec(`ALTER TABLE tasks ADD COLUMN checkout_branch TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("failed to add checkout_branch column: %w", err)
	}
	return nil
}

// migrateV15 adds worktree_detached to tasks.
func migrateV15(db *DB) error {
	if _, err := db.sqlDB.Exec(`ALTER TABLE tasks ADD COLUMN worktree_detached INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("failed to add worktree_detached column: %w", err)
	}
	return nil
}

// migrateV16 adds the task_steps table.
func migrateV16(db *DB) error {
	if _, err := db.sqlDB.Exec(`CREATE TABLE IF NOT EXISTS task_steps (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		step_name TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'running',
		context TEXT,
		exit_code INTEGER,
		started_at DATETIME,
		completed_at DATETIME,
		UNIQUE(task_id, step_name)
	)`); err != nil {
		return fmt.Errorf("failed to create task_steps table: %w", err)
	}
	return nil
}

// migrateV17 adds default_branch_mode and default_workflow to projects.
func migrateV17(db *DB) error {
	if _, err := db.sqlDB.Exec(`ALTER TABLE projects ADD COLUMN default_branch_mode INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("failed to add default_branch_mode column to projects: %w", err)
	}
	if _, err := db.sqlDB.Exec(`ALTER TABLE projects ADD COLUMN default_workflow TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("failed to add default_workflow column to projects: %w", err)
	}
	return nil
}

// migrateV18 adds the chats table.
func migrateV18(db *DB) error {
	if _, err := db.sqlDB.Exec(`CREATE TABLE IF NOT EXISTS chats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		session_id TEXT NOT NULL,
		tmux_session_name TEXT,
		step_name TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("failed to create chats table: %w", err)
	}
	if _, err := db.sqlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_chats_task_id ON chats(task_id)`); err != nil {
		return fmt.Errorf("failed to create idx_chats_task_id index: %w", err)
	}
	if _, err := db.sqlDB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_chats_task_step ON chats(task_id, step_name)`); err != nil {
		return fmt.Errorf("failed to create idx_chats_task_step index: %w", err)
	}
	return nil
}

// migrateV19 adds the task_waits_on table for mid-step child-task suspension
// (StatusAwaitingChildren). Distinct from task_dependencies: edges here are
// inserted by create_tasks_and_wait/wait_for_tasks at suspend time and
// removed automatically when the parent resumes.
func migrateV19(db *DB) error {
	if _, err := db.sqlDB.Exec(`CREATE TABLE IF NOT EXISTS task_waits_on (
		task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		waits_on_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		PRIMARY KEY (task_id, waits_on_id)
	)`); err != nil {
		return fmt.Errorf("failed to create task_waits_on table: %w", err)
	}
	if _, err := db.sqlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_task_waits_on_waits_on ON task_waits_on(waits_on_id)`); err != nil {
		return fmt.Errorf("failed to create idx_task_waits_on_waits_on index: %w", err)
	}
	return nil
}

// migrateV20 adds the tracks table and tasks.track_id. A track is a named,
// mutable, hierarchical context container (see internal/task/track.go); tasks
// attach to one at create time and step prompts opt into its ancestor-
// concatenated context via {{track.*}} template vars. project_id NULL = global
// track (visible from every project). Slug uniqueness is per-scope, which a
// plain UNIQUE(project_id, slug) cannot express (SQLite treats NULLs as
// distinct), hence the two partial unique indexes.
func migrateV20(db *DB) error {
	if _, err := db.sqlDB.Exec(`CREATE TABLE IF NOT EXISTS tracks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id INTEGER REFERENCES projects(id),
		parent_id INTEGER REFERENCES tracks(id),
		name TEXT NOT NULL DEFAULT '',
		slug TEXT NOT NULL DEFAULT '',
		workflow TEXT NOT NULL DEFAULT '',
		context TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("failed to create tracks table: %w", err)
	}
	if _, err := db.sqlDB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_tracks_project_slug ON tracks(project_id, slug) WHERE project_id IS NOT NULL`); err != nil {
		return fmt.Errorf("failed to create idx_tracks_project_slug index: %w", err)
	}
	if _, err := db.sqlDB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_tracks_global_slug ON tracks(slug) WHERE project_id IS NULL`); err != nil {
		return fmt.Errorf("failed to create idx_tracks_global_slug index: %w", err)
	}
	if _, err := db.sqlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_tracks_project_id ON tracks(project_id)`); err != nil {
		return fmt.Errorf("failed to create idx_tracks_project_id index: %w", err)
	}
	if _, err := db.sqlDB.Exec(`ALTER TABLE tasks ADD COLUMN track_id INTEGER REFERENCES tracks(id)`); err != nil {
		return fmt.Errorf("failed to add track_id column: %w", err)
	}
	return nil
}

// migrateV21 adds the description column to tracks: a stable one-liner
// describing the track's purpose, distinct from the accumulating context.
func migrateV21(db *DB) error {
	if _, err := db.sqlDB.Exec(`ALTER TABLE tracks ADD COLUMN description TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("failed to add description column to tracks: %w", err)
	}
	return nil
}

// migrateV22 renames the tasks.description column to tasks.input. The field was
// repurposed: `description` at the workflow/step level is now human-readable
// metadata, while the task's user-supplied input (formerly stored as
// "description") is now "input" — seeding {{task.input}} and step prompts.
// SQLite supports RENAME COLUMN since 3.25 (modernc.org/sqlite is well past it).
//
// The rename is guarded by a column-existence check so replaying the ladder on
// a database that already has the final schema (e.g. a fresh install rolled
// back to an earlier version) is a no-op rather than an error.
func migrateV22(db *DB) error {
	hasDescription, err := db.tasksHasColumn("description")
	if err != nil {
		return fmt.Errorf("failed to inspect tasks columns: %w", err)
	}
	if !hasDescription {
		return nil // already renamed (or a fresh schema that never had it)
	}
	if _, err := db.sqlDB.Exec(`ALTER TABLE tasks RENAME COLUMN description TO input`); err != nil {
		return fmt.Errorf("failed to rename tasks.description to input: %w", err)
	}
	return nil
}

// tasksHasColumn reports whether the tasks table has a column with the given
// name, via PRAGMA table_info.
func (db *DB) tasksHasColumn(name string) (bool, error) {
	rows, err := db.sqlDB.Query(`PRAGMA table_info(tasks)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			colName    string
			colType    string
			notNull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &dfltValue, &primaryKey); err != nil {
			return false, err
		}
		if colName == name {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (db *DB) Path() string {
	return db.path
}
