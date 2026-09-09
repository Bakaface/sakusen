package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CreateTaskStep inserts a new step record with status='running' and started_at=now.
// Uses INSERT OR REPLACE to handle re-runs (e.g., loop iterations).
func (db *DB) CreateTaskStep(taskID int64, stepName string) error {
	_, err := db.sqlDB.Exec(
		`INSERT OR REPLACE INTO task_steps (task_id, step_name, status, started_at)
		 VALUES (?, ?, 'running', ?)`,
		taskID, stepName, time.Now(),
	)
	return err
}

// CompleteTaskStep updates a step record with status='completed', context, exit_code, and completed_at.
func (db *DB) CompleteTaskStep(taskID int64, stepName string, context *string, exitCode int) error {
	_, err := db.sqlDB.Exec(
		`UPDATE task_steps SET status = 'completed', context = ?, exit_code = ?, completed_at = ?
		 WHERE task_id = ? AND step_name = ?`,
		context, exitCode, time.Now(), taskID, stepName,
	)
	return err
}

// FailTaskStep marks a step record 'failed' with its exit code and completion
// time, leaving any context already written on the row intact.
//
// 'failed' is the third task_steps status, introduced for PARALLEL BRANCH rows:
// a group needs to tell "this branch ran and lost" from "this branch never
// started" so the aggregate can mark the gap and the join can count successes.
// Ordinary steps deliberately do NOT use it — a failed step leaves its row at
// 'running', which is what retry-from-step and the TUI already key off.
func (db *DB) FailTaskStep(taskID int64, stepName string, exitCode int) error {
	_, err := db.sqlDB.Exec(
		`UPDATE task_steps SET status = 'failed', exit_code = ?, completed_at = ?
		 WHERE task_id = ? AND step_name = ?`,
		exitCode, time.Now(), taskID, stepName,
	)
	return err
}

// UpdateTaskStepContext overwrites the context for a completed step.
// Used by background summarization to replace the initial last_message context.
func (db *DB) UpdateTaskStepContext(taskID int64, stepName string, context string) error {
	_, err := db.sqlDB.Exec(
		`UPDATE task_steps SET context = ? WHERE task_id = ? AND step_name = ? AND status = 'completed'`,
		context, taskID, stepName,
	)
	return err
}

// UpdateRunningTaskStepContext sets the context of a running step. When
// appendMode is true, value is concatenated to the existing context with a
// newline separator (empty/NULL existing context is treated as the empty
// string, so no leading newline is introduced). Returns the number of rows
// affected — callers can detect "no running step with that name" when this
// is zero. Used by the MCP update_step_context tool so an agent can push the
// canonical artifact for its active step directly, without waiting for the
// post-session summarizer.
func (db *DB) UpdateRunningTaskStepContext(taskID int64, stepName, value string, appendMode bool) (int64, error) {
	return db.updateStepContextWithStatus(taskID, stepName, value, appendMode, "running")
}

// UpdatePausedTmuxStepContext sets the context of a tmux/human step that is
// paused at its approval gate. The engine marks such a step's row 'completed'
// and clears tasks.current_step the instant it spawns the session (so the task
// can pause), yet the agent inside that session is still live — this is the
// path the MCP update_step_context tool uses to let it fold its chat into the
// step context mid-session. The handler resolves and verifies the target is the
// task's active paused step before calling this, so matching 'completed' here
// is scoped to exactly that step. append/return semantics match
// UpdateRunningTaskStepContext.
func (db *DB) UpdatePausedTmuxStepContext(taskID int64, stepName, value string, appendMode bool) (int64, error) {
	return db.updateStepContextWithStatus(taskID, stepName, value, appendMode, "completed")
}

// updateStepContextWithStatus is the shared implementation behind the
// running/paused step-context writers. It updates the context of the single
// task_steps row matching (taskID, stepName, status), supporting both replace
// and newline-joined append. Returns rows affected so callers can detect a
// no-match.
func (db *DB) updateStepContextWithStatus(taskID int64, stepName, value string, appendMode bool, status string) (int64, error) {
	var (
		result sql.Result
		err    error
	)
	if appendMode {
		result, err = db.sqlDB.Exec(
			`UPDATE task_steps
			 SET context = CASE
			     WHEN context IS NULL OR context = '' THEN ?
			     ELSE context || char(10) || ?
			 END
			 WHERE task_id = ? AND step_name = ? AND status = ?`,
			value, value, taskID, stepName, status,
		)
	} else {
		result, err = db.sqlDB.Exec(
			`UPDATE task_steps SET context = ?
			 WHERE task_id = ? AND step_name = ? AND status = ?`,
			value, taskID, stepName, status,
		)
	}
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// GetRunningTaskStepContext returns the context for a single running step.
// A non-empty result indicates a manual override pushed via the
// update_step_context MCP tool while the step was still executing. Returns the
// empty string (no error) when no running row exists or its context is NULL.
func (db *DB) GetRunningTaskStepContext(taskID int64, stepName string) (string, error) {
	var context sql.NullString
	err := db.sqlDB.QueryRow(
		`SELECT context FROM task_steps WHERE task_id = ? AND step_name = ? AND status = 'running'`,
		taskID, stepName,
	).Scan(&context)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	if context.Valid {
		return context.String, nil
	}
	return "", nil
}

// GetTaskStepContext returns the context for a single completed step.
func (db *DB) GetTaskStepContext(taskID int64, stepName string) (string, error) {
	var context sql.NullString
	err := db.sqlDB.QueryRow(
		`SELECT context FROM task_steps WHERE task_id = ? AND step_name = ? AND status = 'completed'`,
		taskID, stepName,
	).Scan(&context)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	if context.Valid {
		return context.String, nil
	}
	return "", nil
}

// GetTaskStepContexts returns contexts for multiple steps (replaces CollectArtifacts).
func (db *DB) GetTaskStepContexts(taskID int64, stepNames []string) (map[string]string, error) {
	if len(stepNames) == 0 {
		return make(map[string]string), nil
	}

	placeholders := make([]string, len(stepNames))
	args := make([]any, 0, len(stepNames)+1)
	args = append(args, taskID)
	for i, name := range stepNames {
		placeholders[i] = "?"
		args = append(args, name)
	}

	query := fmt.Sprintf(
		`SELECT step_name, context FROM task_steps
		 WHERE task_id = ? AND step_name IN (%s) AND status = 'completed' AND context IS NOT NULL`,
		strings.Join(placeholders, ","),
	)

	rows, err := db.sqlDB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var name, ctx string
		if err := rows.Scan(&name, &ctx); err != nil {
			return nil, err
		}
		result[name] = ctx
	}
	return result, rows.Err()
}

// GetAllTaskStepContexts returns all completed step contexts for a task.
func (db *DB) GetAllTaskStepContexts(taskID int64) (map[string]string, error) {
	rows, err := db.sqlDB.Query(
		`SELECT step_name, context FROM task_steps
		 WHERE task_id = ? AND status = 'completed' AND context IS NOT NULL AND context != ''`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var name, ctx string
		if err := rows.Scan(&name, &ctx); err != nil {
			return nil, err
		}
		result[name] = ctx
	}
	return result, rows.Err()
}

// TaskStepRow is the full state of a step persisted in task_steps.
type TaskStepRow struct {
	StepName    string
	Status      string
	Context     string
	CompletedAt sql.NullTime
}

// GetTaskStepRows returns every persisted row for a task, keyed by step_name.
// Includes running and completed steps; the caller is expected to overlay
// these onto the workflow's configured step list.
func (db *DB) GetTaskStepRows(taskID int64) (map[string]TaskStepRow, error) {
	rows, err := db.sqlDB.Query(
		`SELECT step_name, status, context, completed_at FROM task_steps WHERE task_id = ?`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]TaskStepRow)
	for rows.Next() {
		var r TaskStepRow
		var ctx sql.NullString
		if err := rows.Scan(&r.StepName, &r.Status, &ctx, &r.CompletedAt); err != nil {
			return nil, err
		}
		if ctx.Valid {
			r.Context = ctx.String
		}
		result[r.StepName] = r
	}
	return result, rows.Err()
}

// DeleteTaskSteps deletes all step records for a task (used on full retry/reset).
func (db *DB) DeleteTaskSteps(taskID int64) error {
	_, err := db.sqlDB.Exec(`DELETE FROM task_steps WHERE task_id = ?`, taskID)
	return err
}

// DeleteTaskStepsFrom deletes step records from a given index onward (for retry-from-step).
func (db *DB) DeleteTaskStepsFrom(taskID int64, stepNames []string) error {
	if len(stepNames) == 0 {
		return nil
	}
	placeholders := make([]string, len(stepNames))
	args := make([]any, 0, len(stepNames)+1)
	args = append(args, taskID)
	for i, name := range stepNames {
		placeholders[i] = "?"
		args = append(args, name)
	}
	query := fmt.Sprintf(
		`DELETE FROM task_steps WHERE task_id = ? AND step_name IN (%s)`,
		strings.Join(placeholders, ","),
	)
	_, err := db.sqlDB.Exec(query, args...)
	return err
}
