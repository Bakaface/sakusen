package db

import (
	"database/sql"
	"strings"
	"time"
)

// Chat represents a Claude Code chat session associated with a task step.
type Chat struct {
	ID              int64
	TaskID          int64
	SessionID       string
	TmuxSessionName string
	StepName        string
	CreatedAt       time.Time
}

// UpsertChat inserts or updates a chat record for the given task and step.
// If a chat for this task/step combination already exists, it updates the session_id,
// tmux_session_name, and created_at.
func (db *DB) UpsertChat(taskID int64, stepName, sessionID, tmuxSessionName string) error {
	_, err := db.sqlDB.Exec(
		`INSERT INTO chats (task_id, step_name, session_id, tmux_session_name)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(task_id, step_name) DO UPDATE SET
		   session_id = excluded.session_id,
		   tmux_session_name = excluded.tmux_session_name,
		   created_at = CURRENT_TIMESTAMP`,
		taskID, stepName, sessionID, tmuxSessionName,
	)
	return err
}

// SetChatSessionID records the authoritative agent session id for a task step,
// preserving any tmux_session_name already stored. Used to record the session
// id carried in a tmux step's turn-end sentinel payload. Unlike UpsertChat it
// does not touch tmux_session_name or created_at, so it never reorders chats
// or wipes the recorded tmux session name.
func (db *DB) SetChatSessionID(taskID int64, stepName, sessionID string) error {
	_, err := db.sqlDB.Exec(
		`INSERT INTO chats (task_id, step_name, session_id)
		 VALUES (?, ?, ?)
		 ON CONFLICT(task_id, step_name) DO UPDATE SET
		   session_id = excluded.session_id`,
		taskID, stepName, sessionID,
	)
	return err
}

// GetLatestChat returns the most recently created chat for a task.
func (db *DB) GetLatestChat(taskID int64) (*Chat, error) {
	var c Chat
	var tmuxSessionName sql.NullString
	var stepName sql.NullString

	err := db.sqlDB.QueryRow(
		`SELECT id, task_id, session_id, tmux_session_name, step_name, created_at
		 FROM chats WHERE task_id = ? ORDER BY created_at DESC LIMIT 1`,
		taskID,
	).Scan(&c.ID, &c.TaskID, &c.SessionID, &tmuxSessionName, &stepName, &c.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	if tmuxSessionName.Valid {
		c.TmuxSessionName = tmuxSessionName.String
	}
	if stepName.Valid {
		c.StepName = stepName.String
	}
	return &c, nil
}

// GetChatByStep returns the chat for a specific task and step.
func (db *DB) GetChatByStep(taskID int64, stepName string) (*Chat, error) {
	var c Chat
	var tmuxSessionName sql.NullString
	var sn sql.NullString

	err := db.sqlDB.QueryRow(
		`SELECT id, task_id, session_id, tmux_session_name, step_name, created_at
		 FROM chats WHERE task_id = ? AND step_name = ?`,
		taskID, stepName,
	).Scan(&c.ID, &c.TaskID, &c.SessionID, &tmuxSessionName, &sn, &c.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	if tmuxSessionName.Valid {
		c.TmuxSessionName = tmuxSessionName.String
	}
	if sn.Valid {
		c.StepName = sn.String
	}
	return &c, nil
}

// GetChatsByTask returns all chats for a task ordered by creation time ascending.
func (db *DB) GetChatsByTask(taskID int64) ([]*Chat, error) {
	rows, err := db.sqlDB.Query(
		`SELECT id, task_id, session_id, tmux_session_name, step_name, created_at
		 FROM chats WHERE task_id = ? ORDER BY created_at ASC`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chats []*Chat
	for rows.Next() {
		var c Chat
		var tmuxSessionName sql.NullString
		var stepName sql.NullString

		if err := rows.Scan(&c.ID, &c.TaskID, &c.SessionID, &tmuxSessionName, &stepName, &c.CreatedAt); err != nil {
			return nil, err
		}
		if tmuxSessionName.Valid {
			c.TmuxSessionName = tmuxSessionName.String
		}
		if stepName.Valid {
			c.StepName = stepName.String
		}
		chats = append(chats, &c)
	}
	return chats, rows.Err()
}

// DeleteChatsForTask deletes all chat records for a task.
func (db *DB) DeleteChatsForTask(taskID int64) error {
	_, err := db.sqlDB.Exec(`DELETE FROM chats WHERE task_id = ?`, taskID)
	return err
}

// DeleteChatsForSteps deletes chat records for a task scoped to the given
// step names. Used by partial retry: chats for steps before the retry point
// are preserved so {{step_chats.X.session_id}}-style references (if any) and
// /continue resumes for earlier steps still resolve.
func (db *DB) DeleteChatsForSteps(taskID int64, stepNames []string) error {
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
	query := `DELETE FROM chats WHERE task_id = ? AND step_name IN (` + strings.Join(placeholders, ",") + `)`
	_, err := db.sqlDB.Exec(query, args...)
	return err
}
