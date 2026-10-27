package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Bakaface/sakusen/internal/task"
)

// PublishItems stores items as taskID's published list for step, replacing
// any earlier list. Items are stored as given; callers validate them with
// task.NormalizeItems first.
func (db *DB) PublishItems(taskID int64, step string, items []task.PublishedItem) error {
	data, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("failed to marshal published items: %w", err)
	}
	_, err = db.sqlDB.Exec(`
		INSERT INTO task_published_items (task_id, step_name, items_json) VALUES (?, ?, ?)
		ON CONFLICT(task_id, step_name) DO UPDATE SET items_json = excluded.items_json, published_at = CURRENT_TIMESTAMP`,
		taskID, step, string(data))
	return err
}

// GetPublishedItems returns taskID's published list for step; ok is false
// when nothing has been published.
func (db *DB) GetPublishedItems(taskID int64, step string) (items []task.PublishedItem, ok bool, err error) {
	var data string
	err = db.sqlDB.QueryRow(`SELECT items_json FROM task_published_items WHERE task_id = ? AND step_name = ?`, taskID, step).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal([]byte(data), &items); err != nil {
		return nil, false, fmt.Errorf("failed to unmarshal published items: %w", err)
	}
	return items, true, nil
}

// GetAllPublishedItems returns every published list of taskID keyed by step.
func (db *DB) GetAllPublishedItems(taskID int64) (map[string][]task.PublishedItem, error) {
	rows, err := db.sqlDB.Query(`SELECT step_name, items_json FROM task_published_items WHERE task_id = ?`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string][]task.PublishedItem)
	for rows.Next() {
		var step, data string
		if err := rows.Scan(&step, &data); err != nil {
			return nil, err
		}
		var items []task.PublishedItem
		if err := json.Unmarshal([]byte(data), &items); err != nil {
			return nil, fmt.Errorf("failed to unmarshal published items for step %q: %w", step, err)
		}
		out[step] = items
	}
	return out, rows.Err()
}

// ClearPublishedItems drops taskID's published list for step. Idempotent.
func (db *DB) ClearPublishedItems(taskID int64, step string) error {
	_, err := db.sqlDB.Exec(`DELETE FROM task_published_items WHERE task_id = ? AND step_name = ?`, taskID, step)
	return err
}
