package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Bakaface/sortie/internal/task"
)

func (db *DB) CreateTask(projectID int64, title, description, slug, workflow, branch string, status task.Status, images []string) (*task.Task, error) {
	return db.CreateTaskWithPriority(projectID, title, description, slug, workflow, "", branch, "", "", status, task.PriorityMedium, true, images, nil)
}

func (db *DB) CreateTaskWithPriority(projectID int64, title, description, slug, workflow, branchName, branch, targetBranch, checkoutBranch string, status task.Status, priority task.Priority, worktree bool, images []string, trackID *int64) (*task.Task, error) {
	var imagesJSON *string
	if len(images) > 0 {
		data, err := json.Marshal(images)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal images: %w", err)
		}
		s := string(data)
		imagesJSON = &s
	}

	worktreeInt := 0
	if worktree {
		worktreeInt = 1
	}

	result, err := db.sqlDB.Exec(
		`INSERT INTO tasks (project_id, title, description, slug, workflow, branch_name, branch, target_branch, checkout_branch, status, priority, worktree, images, track_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, title, description, slug, workflow, branchName, branch, targetBranch, checkoutBranch, status, priority, worktreeInt, imagesJSON, trackID,
	)
	if err != nil {
		return nil, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}

	return db.GetTask(id)
}

const taskColumns = `id, project_id, track_id, title, description, slug, workflow, status, priority, step_index, current_step, loop_iteration,
	branch_name, branch, target_branch, checkout_branch, worktree, worktree_path, worktree_detached, exit_code, error_message, context, images, commits,
	created_at, started_at, completed_at, updated_at`

func (db *DB) GetTask(id int64) (*task.Task, error) {
	row := db.sqlDB.QueryRow(fmt.Sprintf(`SELECT %s FROM tasks WHERE id = ?`, taskColumns), id)
	t, err := scanTask(row)
	if err != nil {
		return nil, err
	}

	deps, err := db.getBlockedBy(id)
	if err != nil {
		return nil, err
	}
	t.BlockedBy = deps

	return t, nil
}

func (db *DB) getBlockedBy(taskID int64) ([]int64, error) {
	rows, err := db.sqlDB.Query(`SELECT blocked_by FROM task_dependencies WHERE task_id = ?`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deps []int64
	for rows.Next() {
		var dep int64
		if err := rows.Scan(&dep); err != nil {
			return nil, err
		}
		deps = append(deps, dep)
	}
	return deps, rows.Err()
}

func (db *DB) GetPendingTasks() ([]*task.Task, error) {
	return db.getTasksByStatus(task.StatusPending)
}

func (db *DB) GetRunningTasks() ([]*task.Task, error) {
	return db.getTasksByStatus(task.StatusRunning)
}

func (db *DB) GetClaimableTasks() ([]*task.Task, error) {
	rows, err := db.sqlDB.Query(fmt.Sprintf(`
		SELECT %s FROM tasks
		WHERE status = ?
		AND id NOT IN (
			SELECT td.task_id FROM task_dependencies td
			JOIN tasks dep ON dep.id = td.blocked_by
			WHERE dep.status != ?
		)
		ORDER BY
			CASE priority
				WHEN 'urgent' THEN 4
				WHEN 'high' THEN 3
				WHEN 'medium' THEN 2
				WHEN 'low' THEN 1
				ELSE 2
			END DESC,
			created_at ASC
	`, taskColumns), task.StatusPending, task.StatusCompleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanTasks(rows)
}

func (db *DB) GetAllTasks() ([]*task.Task, error) {
	rows, err := db.sqlDB.Query(fmt.Sprintf(`SELECT %s FROM tasks ORDER BY (status = 'tmux') DESC, id DESC`, taskColumns))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}

	// Load dependencies for all tasks
	for _, t := range tasks {
		deps, err := db.getBlockedBy(t.ID)
		if err != nil {
			return nil, err
		}
		t.BlockedBy = deps
	}

	return tasks, nil
}

// GetTasksByProject returns all tasks for a specific project.
func (db *DB) GetTasksByProject(projectID int64) ([]*task.Task, error) {
	rows, err := db.sqlDB.Query(fmt.Sprintf(`SELECT %s FROM tasks WHERE project_id = ? ORDER BY (status = 'tmux') DESC, id DESC`, taskColumns), projectID)
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

// GetTasksByProjectName returns all tasks for projects matching the given name.
func (db *DB) GetTasksByProjectName(name string) ([]*task.Task, error) {
	rows, err := db.sqlDB.Query(fmt.Sprintf(`SELECT %s FROM tasks WHERE project_id IN (SELECT id FROM projects WHERE name = ?) ORDER BY (status = 'tmux') DESC, id DESC`, taskColumns), name)
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

func (db *DB) getTasksByStatus(status task.Status) ([]*task.Task, error) {
	rows, err := db.sqlDB.Query(fmt.Sprintf(`
		SELECT %s FROM tasks WHERE status = ? ORDER BY id ASC
	`, taskColumns), status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanTasks(rows)
}

// ClaimTask atomically transitions a task from pending to running.
// Returns true if the claim succeeded, false if the task was not in pending state.
func (db *DB) ClaimTask(id int64) (bool, error) {
	now := time.Now()
	result, err := db.sqlDB.Exec(
		"UPDATE tasks SET status = ?, started_at = ?, updated_at = ? WHERE id = ? AND status = ?",
		task.StatusRunning, now, now, id, task.StatusPending,
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

func (db *DB) UpdateTaskStatus(id int64, status task.Status) error {
	now := time.Now()
	var query string
	var args []interface{}

	switch status {
	case task.StatusRunning:
		query = "UPDATE tasks SET status = ?, started_at = ?, updated_at = ? WHERE id = ?"
		args = []interface{}{status, now, now, id}
	case task.StatusCompleted, task.StatusFailed:
		query = "UPDATE tasks SET status = ?, completed_at = ?, updated_at = ? WHERE id = ?"
		args = []interface{}{status, now, now, id}
	default:
		query = "UPDATE tasks SET status = ?, updated_at = ? WHERE id = ?"
		args = []interface{}{status, now, id}
	}

	_, err := db.sqlDB.Exec(query, args...)
	return err
}

func (db *DB) UpdateTaskWorktreePath(id int64, worktreePath string) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET worktree_path = ?, updated_at = ? WHERE id = ?",
		worktreePath, time.Now(), id,
	)
	return err
}

func (db *DB) UpdateTaskBranch(id int64, branch string) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET branch = ?, updated_at = ? WHERE id = ?",
		branch, time.Now(), id,
	)
	return err
}

func (db *DB) ClearWorktreePath(id int64) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET worktree_path = NULL, updated_at = ? WHERE id = ?",
		time.Now(), id,
	)
	return err
}

func (db *DB) UpdateTaskStep(id int64, stepIndex int, currentStep string) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET step_index = ?, current_step = ?, updated_at = ? WHERE id = ?",
		stepIndex, currentStep, time.Now(), id,
	)
	return err
}

func (db *DB) UpdateTaskExitCode(id int64, exitCode int, errorMessage string) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET exit_code = ?, error_message = ?, updated_at = ? WHERE id = ?",
		exitCode, errorMessage, time.Now(), id,
	)
	return err
}

func (db *DB) UpdateTaskError(id int64, errMsg string) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET error_message = ?, status = ?, updated_at = ? WHERE id = ?",
		errMsg, task.StatusFailed, time.Now(), id,
	)
	return err
}

// UpdateTaskStatusError records an error message alongside a caller-supplied
// terminal status in a single write, so a poller can never observe the new
// status without its reason (or vice versa).
func (db *DB) UpdateTaskStatusError(id int64, status task.Status, errMsg string) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET error_message = ?, status = ?, updated_at = ? WHERE id = ?",
		errMsg, status, time.Now(), id,
	)
	return err
}

func (db *DB) UpdateTaskPriority(id int64, priority task.Priority) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET priority = ?, updated_at = ? WHERE id = ?",
		priority, time.Now(), id,
	)
	return err
}

func (db *DB) UpdateTaskContext(id int64, taskContext string) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET context = ?, updated_at = ? WHERE id = ?",
		taskContext, time.Now(), id,
	)
	return err
}

func (db *DB) UpdateTaskTitle(id int64, title string) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET title = ?, updated_at = ? WHERE id = ?",
		title, time.Now(), id,
	)
	return err
}

func (db *DB) UpdateTaskDescription(id int64, description string) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET description = ?, updated_at = ? WHERE id = ?",
		description, time.Now(), id,
	)
	return err
}

func (db *DB) FinalizeTaskIdentity(id int64, title, slug, branch string) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET title = ?, slug = ?, branch = ?, updated_at = ? WHERE id = ?",
		title, slug, branch, time.Now(), id,
	)
	return err
}

func (db *DB) UpdateTaskLoopIteration(id int64, iteration int) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET loop_iteration = ?, updated_at = ? WHERE id = ?",
		iteration, time.Now(), id,
	)
	return err
}

func (db *DB) ResetTaskForRetry(id int64) error {
	_, err := db.sqlDB.Exec(
		`UPDATE tasks SET status = ?, step_index = 0, current_step = NULL, loop_iteration = 0,
		 exit_code = NULL, error_message = NULL, started_at = NULL,
		 completed_at = NULL, updated_at = ? WHERE id = ?`,
		task.StatusPending, time.Now(), id,
	)
	if err != nil {
		return err
	}
	if err := db.DeleteTaskSteps(id); err != nil {
		return err
	}
	return db.DeleteChatsForTask(id)
}

func (db *DB) ResetTaskForRetryFromStep(id int64) error {
	_, err := db.sqlDB.Exec(
		`UPDATE tasks SET status = ?, current_step = NULL,
		 exit_code = NULL, error_message = NULL, started_at = NULL,
		 completed_at = NULL, updated_at = ? WHERE id = ?`,
		task.StatusPending, time.Now(), id,
	)
	if err != nil {
		return err
	}
	// Delete all step records since re-running from a step may re-execute prior steps in loops.
	if err := db.DeleteTaskSteps(id); err != nil {
		return err
	}
	return db.DeleteChatsForTask(id)
}

// ResetTaskForRetryAtStep prepares a task to be re-run starting at a specific
// workflow step. Unlike ResetTaskForRetry, completed work for steps preceding
// stepIdx is preserved: their step_steps rows (with captured contexts) and
// chats remain, so later steps' template references like
// {{step_contexts.<name>}} continue to resolve. Step records and chats for
// stepNames (which should be the step at stepIdx and all later steps) are
// deleted so they will be regenerated by the engine.
func (db *DB) ResetTaskForRetryAtStep(id int64, stepIdx int, stepNames []string) error {
	_, err := db.sqlDB.Exec(
		`UPDATE tasks SET status = ?, step_index = ?, current_step = NULL, loop_iteration = 0,
		 exit_code = NULL, error_message = NULL, started_at = NULL,
		 completed_at = NULL, updated_at = ? WHERE id = ?`,
		task.StatusPending, stepIdx, time.Now(), id,
	)
	if err != nil {
		return err
	}
	if err := db.DeleteTaskStepsFrom(id, stepNames); err != nil {
		return err
	}
	return db.DeleteChatsForSteps(id, stepNames)
}

func (db *DB) ResetTaskForContinue(id int64, workflow, prompt string) error {
	if prompt != "" {
		_, err := db.sqlDB.Exec(
			`UPDATE tasks SET status = ?, workflow = ?, description = ?, step_index = 0, current_step = NULL, loop_iteration = 0,
			 exit_code = NULL, error_message = NULL, started_at = NULL,
			 completed_at = NULL, updated_at = ? WHERE id = ?`,
			task.StatusPending, workflow, prompt, time.Now(), id,
		)
		if err != nil {
			return err
		}
		if err := db.DeleteTaskSteps(id); err != nil {
			return err
		}
		return db.DeleteChatsForTask(id)
	}
	_, err := db.sqlDB.Exec(
		`UPDATE tasks SET status = ?, workflow = ?, step_index = 0, current_step = NULL, loop_iteration = 0,
		 exit_code = NULL, error_message = NULL, started_at = NULL,
		 completed_at = NULL, updated_at = ? WHERE id = ?`,
		task.StatusPending, workflow, time.Now(), id,
	)
	if err != nil {
		return err
	}
	if err := db.DeleteTaskSteps(id); err != nil {
		return err
	}
	return db.DeleteChatsForTask(id)
}

func (db *DB) AppendTaskCommit(id int64, commitHash string) error {
	t, err := db.GetTask(id)
	if err != nil {
		return err
	}
	commits := append(t.Commits, commitHash)
	data, err := json.Marshal(commits)
	if err != nil {
		return fmt.Errorf("failed to marshal commits: %w", err)
	}
	_, err = db.sqlDB.Exec(
		"UPDATE tasks SET commits = ?, updated_at = ? WHERE id = ?",
		string(data), time.Now(), id,
	)
	return err
}

func (db *DB) GetTaskCommits(id int64) ([]string, error) {
	var commitsJSON sql.NullString
	err := db.sqlDB.QueryRow("SELECT commits FROM tasks WHERE id = ?", id).Scan(&commitsJSON)
	if err != nil {
		return nil, err
	}
	if !commitsJSON.Valid || commitsJSON.String == "" {
		return nil, nil
	}
	var commits []string
	if err := json.Unmarshal([]byte(commitsJSON.String), &commits); err != nil {
		return nil, fmt.Errorf("failed to unmarshal commits: %w", err)
	}
	return commits, nil
}

// AddTaskDependency adds a dependency: taskID is blocked by blockedByID.
func (db *DB) AddTaskDependency(taskID, blockedByID int64) error {
	_, err := db.sqlDB.Exec(`INSERT OR IGNORE INTO task_dependencies (task_id, blocked_by) VALUES (?, ?)`, taskID, blockedByID)
	return err
}

// RemoveTaskDependency removes a dependency: taskID is no longer blocked by blockedByID.
func (db *DB) RemoveTaskDependency(taskID, blockedByID int64) error {
	_, err := db.sqlDB.Exec(`DELETE FROM task_dependencies WHERE task_id = ? AND blocked_by = ?`, taskID, blockedByID)
	return err
}

// SetTaskDependencies replaces all dependencies for a task with the given list.
func (db *DB) SetTaskDependencies(taskID int64, blockedBy []int64) error {
	tx, err := db.sqlDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM task_dependencies WHERE task_id = ?`, taskID); err != nil {
		return err
	}

	for _, depID := range blockedBy {
		if _, err := tx.Exec(`INSERT INTO task_dependencies (task_id, blocked_by) VALUES (?, ?)`, taskID, depID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// HasCircularDependency checks if adding an edge (taskID blocked by newBlockedByID)
// would create a cycle. It does BFS from newBlockedByID through existing blocked_by
// chains and returns true if taskID is reachable.
func (db *DB) HasCircularDependency(taskID, newBlockedByID int64) (bool, error) {
	// If taskID == newBlockedByID, it's trivially circular
	if taskID == newBlockedByID {
		return true, nil
	}

	// BFS: start from newBlockedByID and follow blocked_by edges
	// (i.e., find what blocks newBlockedByID, then what blocks those, etc.)
	// If we reach taskID, adding the edge would create a cycle.
	visited := map[int64]bool{newBlockedByID: true}
	queue := []int64{newBlockedByID}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		rows, err := db.sqlDB.Query(`SELECT blocked_by FROM task_dependencies WHERE task_id = ?`, current)
		if err != nil {
			return false, err
		}

		for rows.Next() {
			var dep int64
			if err := rows.Scan(&dep); err != nil {
				rows.Close()
				return false, err
			}
			if dep == taskID {
				rows.Close()
				return true, nil
			}
			if !visited[dep] {
				visited[dep] = true
				queue = append(queue, dep)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return false, err
		}
	}

	return false, nil
}

func (db *DB) DeleteTask(id int64) error {
	_, err := db.sqlDB.Exec("DELETE FROM chats WHERE task_id = ?", id)
	if err != nil {
		return err
	}
	_, err = db.sqlDB.Exec("DELETE FROM task_dependencies WHERE task_id = ? OR blocked_by = ?", id, id)
	if err != nil {
		return err
	}
	_, err = db.sqlDB.Exec("DELETE FROM task_waits_on WHERE task_id = ? OR waits_on_id = ?", id, id)
	if err != nil {
		return err
	}
	_, err = db.sqlDB.Exec("DELETE FROM tasks WHERE id = ?", id)
	return err
}

// scanner is the common interface between *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanTaskRow(s scanner) (*task.Task, error) {
	var t task.Task
	var projectID sql.NullInt64
	var trackID sql.NullInt64
	var title, slug, workflow, branchName, branch sql.NullString
	var targetBranch, checkoutBranch sql.NullString
	var priority sql.NullString
	var currentStep, worktreePath, errorMessage sql.NullString
	var taskContext sql.NullString
	var imagesJSON sql.NullString
	var commitsJSON sql.NullString
	var exitCode sql.NullInt64
	var worktreeInt int
	var worktreeDetached int
	var startedAt, completedAt sql.NullTime
	var updatedAt sql.NullTime

	err := s.Scan(
		&t.ID, &projectID, &trackID, &title, &t.Description, &slug, &workflow, &t.Status, &priority,
		&t.StepIndex, &currentStep, &t.LoopIteration,
		&branchName, &branch, &targetBranch, &checkoutBranch, &worktreeInt, &worktreePath, &worktreeDetached, &exitCode, &errorMessage, &taskContext, &imagesJSON, &commitsJSON,
		&t.CreatedAt, &startedAt, &completedAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}

	t.Worktree = worktreeInt != 0
	t.WorktreeDetached = worktreeDetached != 0
	if projectID.Valid {
		t.ProjectID = projectID.Int64
	}
	if trackID.Valid {
		v := trackID.Int64
		t.TrackID = &v
	}
	if title.Valid {
		t.Title = title.String
	}
	if slug.Valid {
		t.Slug = slug.String
	}
	if workflow.Valid {
		t.Workflow = workflow.String
	}
	if priority.Valid {
		t.Priority = task.Priority(priority.String)
	} else {
		t.Priority = task.PriorityMedium
	}
	if currentStep.Valid {
		t.CurrentStep = currentStep.String
	}
	if branchName.Valid {
		t.BranchName = branchName.String
	}
	if branch.Valid {
		t.Branch = branch.String
	}
	if targetBranch.Valid {
		t.TargetBranch = targetBranch.String
	}
	if checkoutBranch.Valid {
		t.CheckoutBranch = checkoutBranch.String
	}
	if worktreePath.Valid {
		t.WorktreePath = worktreePath.String
	}
	if exitCode.Valid {
		code := int(exitCode.Int64)
		t.ExitCode = &code
	}
	if errorMessage.Valid {
		t.ErrorMessage = errorMessage.String
	}
	if taskContext.Valid {
		t.Context = taskContext.String
	}
	if imagesJSON.Valid && imagesJSON.String != "" {
		if err := json.Unmarshal([]byte(imagesJSON.String), &t.Images); err != nil {
			return nil, fmt.Errorf("failed to unmarshal images: %w", err)
		}
	}
	if commitsJSON.Valid && commitsJSON.String != "" {
		if err := json.Unmarshal([]byte(commitsJSON.String), &t.Commits); err != nil {
			return nil, fmt.Errorf("failed to unmarshal commits: %w", err)
		}
	}
	if startedAt.Valid {
		t.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		t.CompletedAt = &completedAt.Time
	}
	if updatedAt.Valid {
		t.UpdatedAt = updatedAt.Time
	}

	return &t, nil
}

func scanTask(row *sql.Row) (*task.Task, error) {
	return scanTaskRow(row)
}

func scanTasks(rows *sql.Rows) ([]*task.Task, error) {
	var tasks []*task.Task
	for rows.Next() {
		t, err := scanTaskRow(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (db *DB) SetWorktreeDetached(id int64, detached bool) error {
	val := 0
	if detached {
		val = 1
	}
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET worktree_detached = ?, updated_at = ? WHERE id = ?",
		val, time.Now(), id,
	)
	return err
}
