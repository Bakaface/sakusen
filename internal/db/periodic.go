package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

// PeriodicDef is a row in periodic_definitions: a routine reconciled from the
// top-level routines: section of .sakusen.yml. Every routine gets a row so run
// history and last-task tracking are uniform; a row with an empty cadence is
// on-demand only and never becomes due.
type PeriodicDef struct {
	ID        int64
	ProjectID int64
	Name      string
	// Cadence is the routine's cron/@every spec. Empty ⇒ on-demand only.
	Cadence     string
	WorkflowRef string
	Input       string
	// Priority is stored raw from .sakusen.yml. Empty ⇒ fall back to the
	// project default at fire time (resolved in createTaskFromRequest), so a
	// changed project default takes effect without a config touch.
	Priority string
	Paused   bool
	// ConfigPaused mirrors the last-reconciled paused value from .sakusen.yml.
	// It is compared against the incoming YAML value to detect an actual config
	// change, so interactive pause/resume (which only touches Paused) is not
	// clobbered by reconciliation. See UpsertPeriodicDef.
	ConfigPaused bool
	NextFireAt   time.Time
	LastFiredAt  *time.Time
	LastTaskID   *int64
	DeletedAt    *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const periodicColumns = `id, project_id, name, cadence, workflow_ref, input, priority, paused, config_paused,
	next_fire_at, last_fired_at, last_task_id, deleted_at, created_at, updated_at`

func scanPeriodicDef(s scanner) (*PeriodicDef, error) {
	var p PeriodicDef
	var workflowRef, input, priority sql.NullString
	var pausedInt, configPausedInt int
	var lastFiredAt, deletedAt sql.NullTime
	var lastTaskID sql.NullInt64
	var createdAt, updatedAt sql.NullTime

	if err := s.Scan(
		&p.ID, &p.ProjectID, &p.Name, &p.Cadence, &workflowRef, &input, &priority, &pausedInt, &configPausedInt,
		&p.NextFireAt, &lastFiredAt, &lastTaskID, &deletedAt, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	if workflowRef.Valid {
		p.WorkflowRef = workflowRef.String
	}
	if input.Valid {
		p.Input = input.String
	}
	if priority.Valid {
		p.Priority = priority.String
	}
	p.Paused = pausedInt != 0
	p.ConfigPaused = configPausedInt != 0
	if lastFiredAt.Valid {
		t := lastFiredAt.Time
		p.LastFiredAt = &t
	}
	if lastTaskID.Valid {
		id := lastTaskID.Int64
		p.LastTaskID = &id
	}
	if deletedAt.Valid {
		t := deletedAt.Time
		p.DeletedAt = &t
	}
	if createdAt.Valid {
		p.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		p.UpdatedAt = updatedAt.Time
	}
	return &p, nil
}

func scanPeriodicDefs(rows *sql.Rows) ([]*PeriodicDef, error) {
	var defs []*PeriodicDef
	for rows.Next() {
		d, err := scanPeriodicDef(rows)
		if err != nil {
			return nil, err
		}
		defs = append(defs, d)
	}
	return defs, rows.Err()
}

// GetPeriodicByID returns a periodic definition by id (including soft-deleted).
func (db *DB) GetPeriodicByID(id int64) (*PeriodicDef, error) {
	return scanPeriodicDef(db.sqlDB.QueryRow(
		fmt.Sprintf(`SELECT %s FROM periodic_definitions WHERE id = ?`, periodicColumns), id,
	))
}

// GetPeriodicByName returns a periodic definition by (project_id, name),
// including soft-deleted rows. Returns sql.ErrNoRows when absent.
func (db *DB) GetPeriodicByName(projectID int64, name string) (*PeriodicDef, error) {
	return scanPeriodicDef(db.sqlDB.QueryRow(
		fmt.Sprintf(`SELECT %s FROM periodic_definitions WHERE project_id = ? AND name = ?`, periodicColumns),
		projectID, name,
	))
}

// ListPeriodicsForProject returns all non-deleted periodic definitions for a
// project, ordered by name.
func (db *DB) ListPeriodicsForProject(projectID int64) ([]*PeriodicDef, error) {
	rows, err := db.sqlDB.Query(
		fmt.Sprintf(`SELECT %s FROM periodic_definitions WHERE project_id = ? AND deleted_at IS NULL ORDER BY name ASC`, periodicColumns),
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPeriodicDefs(rows)
}

// ListDuePeriodics returns active (non-deleted, non-paused) scheduled routines
// whose next_fire_at is at or before `now`. Cadence-less (on-demand) rows are
// excluded: they carry a next_fire_at only because the column is NOT NULL.
func (db *DB) ListDuePeriodics(now time.Time) ([]*PeriodicDef, error) {
	rows, err := db.sqlDB.Query(
		fmt.Sprintf(`SELECT %s FROM periodic_definitions WHERE deleted_at IS NULL AND paused = 0 AND cadence != '' AND next_fire_at <= ? ORDER BY next_fire_at ASC`, periodicColumns),
		now,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPeriodicDefs(rows)
}

// UpsertPeriodicDef inserts a new definition or updates an existing one for
// (projectID, name). On update it refreshes cadence/workflow_ref/input/priority
// and clears deleted_at (so re-appearing entries revive with their run history
// intact). next_fire_at is recomputed (set to nextFireAt) only when the cadence
// changed or the row was previously soft-deleted; otherwise the existing
// schedule is preserved. An empty cadence (on-demand routine) still stores
// nextFireAt, which the due-query ignores.
//
// The `paused` argument is the value declared in .sakusen.yml. It is applied to
// the runtime flag using a deferred-override approach that mirrors the cadence
// logic: on INSERT both `paused` and `config_paused` are seeded from YAML; on
// UPDATE the runtime `paused` flag is overridden only when the YAML value
// actually changed since the last reconcile (paused != config_paused). This
// lets users resume/pause from YAML while still preserving an interactive
// pause/resume (SetPeriodicPaused) that has not been countermanded in the
// config. Returns the resulting definition.
func (db *DB) UpsertPeriodicDef(projectID int64, name, cadence, workflowRef, input, priority string, paused bool, nextFireAt time.Time) (*PeriodicDef, error) {
	now := time.Now()

	existing, err := db.GetPeriodicByName(projectID, name)
	if err == sql.ErrNoRows {
		pausedInt := boolToInt(paused)
		_, err := db.sqlDB.Exec(
			`INSERT INTO periodic_definitions (project_id, name, cadence, workflow_ref, input, priority, paused, config_paused, next_fire_at, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			projectID, name, cadence, workflowRef, input, priority, pausedInt, pausedInt, nextFireAt, now, now,
		)
		if err != nil {
			return nil, err
		}
		return db.GetPeriodicByName(projectID, name)
	}
	if err != nil {
		return nil, err
	}

	recomputeFire := existing.Cadence != cadence || existing.DeletedAt != nil

	// Detect whether the YAML paused value changed since the last reconcile.
	// Only then do we override the runtime flag (and re-seed config_paused);
	// otherwise an interactive pause/resume survives reconciliation untouched.
	pausedChanged := paused != existing.ConfigPaused

	// Build the SET clause incrementally so we touch next_fire_at / paused only
	// when they actually need to change.
	set := "cadence = ?, workflow_ref = ?, input = ?, priority = ?, deleted_at = NULL, updated_at = ?"
	args := []any{cadence, workflowRef, input, priority, now}
	if recomputeFire {
		set += ", next_fire_at = ?"
		args = append(args, nextFireAt)
	}
	if pausedChanged {
		set += ", paused = ?, config_paused = ?"
		args = append(args, boolToInt(paused), boolToInt(paused))
	}
	args = append(args, existing.ID)

	if _, err = db.sqlDB.Exec(
		fmt.Sprintf(`UPDATE periodic_definitions SET %s WHERE id = ?`, set), args...,
	); err != nil {
		return nil, err
	}
	return db.GetPeriodicByID(existing.ID)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SoftDeletePeriodicDef marks the named definition deleted (preserving its run
// history). No-op when already deleted or absent.
func (db *DB) SoftDeletePeriodicDef(projectID int64, name string) error {
	_, err := db.sqlDB.Exec(
		`UPDATE periodic_definitions SET deleted_at = ?, updated_at = ? WHERE project_id = ? AND name = ? AND deleted_at IS NULL`,
		time.Now(), time.Now(), projectID, name,
	)
	return err
}

// ClaimPeriodicFire atomically advances a definition's schedule for one fire.
// It sets next_fire_at = newNextFire and last_fired_at = now, but only if the
// row is still due (next_fire_at <= now), scheduled, active, and not paused.
// Returns true
// when the claim succeeded — serializing fires across double-ticks and daemon
// restarts (mirrors ClaimTask).
func (db *DB) ClaimPeriodicFire(id int64, now, newNextFire time.Time) (bool, error) {
	result, err := db.sqlDB.Exec(
		`UPDATE periodic_definitions SET next_fire_at = ?, last_fired_at = ?, updated_at = ?
		 WHERE id = ? AND next_fire_at <= ? AND deleted_at IS NULL AND paused = 0 AND cadence != ''`,
		newNextFire, now, now, id, now,
	)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// SetPeriodicPaused toggles the paused flag for a definition.
func (db *DB) SetPeriodicPaused(id int64, paused bool) error {
	_, err := db.sqlDB.Exec(
		`UPDATE periodic_definitions SET paused = ?, updated_at = ? WHERE id = ?`,
		boolToInt(paused), time.Now(), id,
	)
	return err
}

// UpdatePeriodicLastTask records the most recently materialized task for a
// definition (used for skip-overlap and run-history surfacing).
func (db *DB) UpdatePeriodicLastTask(id, taskID int64) error {
	_, err := db.sqlDB.Exec(
		`UPDATE periodic_definitions SET last_task_id = ?, updated_at = ? WHERE id = ?`,
		taskID, time.Now(), id,
	)
	return err
}

// GetTasksForPeriodic returns all tasks materialized by the given periodic
// definition, newest first.
func (db *DB) GetTasksForPeriodic(periodicID int64) ([]*task.Task, error) {
	rows, err := db.sqlDB.Query(
		fmt.Sprintf(`SELECT %s FROM tasks WHERE periodic_id = ? ORDER BY id DESC`, taskColumns),
		periodicID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}
