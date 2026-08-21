package db

import (
	"fmt"
	"strings"

	"github.com/Bakaface/sakusen/internal/task"
)

// AddTaskWaitsOn records that taskID is suspended waiting for waitsOnID to
// reach a terminal status. INSERT OR IGNORE so re-adding the same edge is a
// no-op.
func (db *DB) AddTaskWaitsOn(taskID, waitsOnID int64) error {
	if taskID == waitsOnID {
		return fmt.Errorf("task cannot wait on itself (task #%d)", taskID)
	}
	_, err := db.sqlDB.Exec(`INSERT OR IGNORE INTO task_waits_on (task_id, waits_on_id) VALUES (?, ?)`, taskID, waitsOnID)
	return err
}

// RemoveTaskWaitsOn drops a single wait-on edge. Idempotent.
func (db *DB) RemoveTaskWaitsOn(taskID, waitsOnID int64) error {
	_, err := db.sqlDB.Exec(`DELETE FROM task_waits_on WHERE task_id = ? AND waits_on_id = ?`, taskID, waitsOnID)
	return err
}

// RemoveAllTaskWaitsOn drops every wait-on edge for taskID. Called when the
// parent resumes — the wait-on edges are an ephemeral lifecycle, not a
// historical record.
func (db *DB) RemoveAllTaskWaitsOn(taskID int64) error {
	_, err := db.sqlDB.Exec(`DELETE FROM task_waits_on WHERE task_id = ?`, taskID)
	return err
}

// GetTaskWaitsOn returns the IDs of children that taskID is currently waiting
// on. Order is unspecified.
func (db *DB) GetTaskWaitsOn(taskID int64) ([]int64, error) {
	rows, err := db.sqlDB.Query(`SELECT waits_on_id FROM task_waits_on WHERE task_id = ? ORDER BY waits_on_id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// HasAnyWaitsOn reports whether taskID has any wait-on edges. Cheap probe
// used by the engine pause branch.
func (db *DB) HasAnyWaitsOn(taskID int64) (bool, error) {
	var count int
	err := db.sqlDB.QueryRow(`SELECT COUNT(*) FROM task_waits_on WHERE task_id = ?`, taskID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// AllWaitsOnTerminal returns true iff every child of taskID has reached a
// terminal status (completed or failed). Empty wait set returns true (the
// parent is not waiting on anyone, so it is trivially unblocked).
//
// Intentionally project-blind: task_waits_on is a pure task-ID relation and a
// parent may wait on children in other projects. Do not add a project filter.
func (db *DB) AllWaitsOnTerminal(taskID int64) (bool, error) {
	rows, err := db.sqlDB.Query(`
		SELECT t.status FROM task_waits_on w
		JOIN tasks t ON t.id = w.waits_on_id
		WHERE w.task_id = ?`, taskID)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return false, err
		}
		if !task.Status(status).IsTerminal() {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return true, nil
}

// GetTasksAwaitingChildren returns all tasks currently in StatusAwaitingChildren.
// Used by the poller to find candidates for resume.
func (db *DB) GetTasksAwaitingChildren() ([]*task.Task, error) {
	return db.getTasksByStatus(task.StatusAwaitingChildren)
}

// GetWaitsOnChildren returns the full Task structs for every child taskID is
// waiting on. Used to build {{children.*}} template variables on resume.
//
// Intentionally project-blind: it feeds {{children.*}} for children in any
// project. Do not add a project filter.
func (db *DB) GetWaitsOnChildren(taskID int64) ([]*task.Task, error) {
	rows, err := db.sqlDB.Query(fmt.Sprintf(`
		SELECT %s FROM tasks
		WHERE id IN (SELECT waits_on_id FROM task_waits_on WHERE task_id = ?)
		ORDER BY id ASC`, taskColumns), taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanTasks(rows)
}

// HasCircularWaitsOn reports whether adding the edge (taskID waits on
// newWaitsOnID) would create a cycle. BFS from newWaitsOnID through existing
// waits_on edges; returns true if taskID is reachable. Also checks the
// task_dependencies graph so a parent cannot wait on one of its own ancestors
// across either relation.
//
// ID-based and therefore already cross-project-safe: a cycle spanning two
// projects is still a cycle in the unified blocking order.
func (db *DB) HasCircularWaitsOn(taskID, newWaitsOnID int64) (bool, error) {
	if taskID == newWaitsOnID {
		return true, nil
	}

	visited := map[int64]bool{newWaitsOnID: true}
	queue := []int64{newWaitsOnID}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		// Walk waits_on edges (current waits on what?).
		rows, err := db.sqlDB.Query(`SELECT waits_on_id FROM task_waits_on WHERE task_id = ?`, current)
		if err != nil {
			return false, err
		}
		for rows.Next() {
			var next int64
			if err := rows.Scan(&next); err != nil {
				rows.Close()
				return false, err
			}
			if next == taskID {
				rows.Close()
				return true, nil
			}
			if !visited[next] {
				visited[next] = true
				queue = append(queue, next)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return false, err
		}

		// Also walk task_dependencies (current blocked_by what?) — a cycle
		// across both graphs is still a cycle in the unified blocking order.
		depRows, err := db.sqlDB.Query(`SELECT blocked_by FROM task_dependencies WHERE task_id = ?`, current)
		if err != nil {
			return false, err
		}
		for depRows.Next() {
			var next int64
			if err := depRows.Scan(&next); err != nil {
				depRows.Close()
				return false, err
			}
			if next == taskID {
				depRows.Close()
				return true, nil
			}
			if !visited[next] {
				visited[next] = true
				queue = append(queue, next)
			}
		}
		depRows.Close()
		if err := depRows.Err(); err != nil {
			return false, err
		}
	}

	return false, nil
}

// columnsForChildren is a convenience for callers that need a comma-separated
// list of placeholders matching the same length as ids; not exported but kept
// near the related queries.
func placeholdersFor(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	out := make([]string, len(ids))
	for i := range ids {
		out[i] = "?"
	}
	return strings.Join(out, ",")
}
