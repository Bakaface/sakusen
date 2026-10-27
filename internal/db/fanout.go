package db

import (
	"fmt"

	"github.com/Bakaface/sakusen/internal/task"
)

// maxFanoutDepthWalk bounds GetFanoutDepth's ancestor walk so a corrupt
// fanout_parent_id loop terminates.
const maxFanoutDepthWalk = 1000

// GetFanoutChildren returns the children parentID's fanout step spawned,
// ordered by id.
func (db *DB) GetFanoutChildren(parentID int64, step string) ([]*task.Task, error) {
	return db.queryFanoutChildren(fmt.Sprintf(`SELECT %s FROM tasks WHERE fanout_parent_id = ? AND fanout_step = ? ORDER BY id ASC`, taskColumns), parentID, step)
}

// GetAllFanoutChildren returns every fanout child of parentID across all its
// fanout steps, ordered by id.
func (db *DB) GetAllFanoutChildren(parentID int64) ([]*task.Task, error) {
	return db.queryFanoutChildren(fmt.Sprintf(`SELECT %s FROM tasks WHERE fanout_parent_id = ? ORDER BY id ASC`, taskColumns), parentID)
}

func (db *DB) queryFanoutChildren(query string, args ...any) ([]*task.Task, error) {
	rows, err := db.sqlDB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		deps, err := db.getBlockedBy(t.ID)
		if err != nil {
			return nil, err
		}
		t.BlockedBy = deps
	}
	return tasks, nil
}

// GetFanoutDepth returns how many fanout_parent_id hops lie above taskID; a
// task with no fanout parent is at depth 0.
func (db *DB) GetFanoutDepth(taskID int64) (int, error) {
	var depth int
	err := db.sqlDB.QueryRow(`
		WITH RECURSIVE ancestors(id, depth) AS (
			SELECT fanout_parent_id, 1 FROM tasks WHERE id = ? AND fanout_parent_id IS NOT NULL
			UNION ALL
			SELECT t.fanout_parent_id, a.depth + 1 FROM tasks t JOIN ancestors a ON t.id = a.id
			WHERE t.fanout_parent_id IS NOT NULL AND a.depth < ?
		)
		SELECT COALESCE(MAX(depth), 0) FROM ancestors`, taskID, maxFanoutDepthWalk).Scan(&depth)
	return depth, err
}

// IncrementFanoutAttempts bumps taskID's fanout attempt counter and returns
// the new value.
func (db *DB) IncrementFanoutAttempts(taskID int64) (int, error) {
	var n int
	err := db.sqlDB.QueryRow(`UPDATE tasks SET fanout_attempts = fanout_attempts + 1 WHERE id = ? RETURNING fanout_attempts`, taskID).Scan(&n)
	return n, err
}

// ResetFanoutAttempts zeroes taskID's fanout attempt counter.
func (db *DB) ResetFanoutAttempts(taskID int64) error {
	_, err := db.sqlDB.Exec(`UPDATE tasks SET fanout_attempts = 0 WHERE id = ?`, taskID)
	return err
}
