package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Bakaface/sakusen/internal/task"
)

func (db *DB) CreateTask(projectID int64, title, input, slug, workflow, branch string, status task.Status, images []string) (*task.Task, error) {
	return db.CreateTaskWithPriority(projectID, title, input, slug, workflow, "", branch, "", "", status, task.PriorityMedium, true, images, nil)
}

func (db *DB) CreateTaskWithPriority(projectID int64, title, input, slug, workflow, branchName, branch, targetBranch, checkoutBranch string, status task.Status, priority task.Priority, worktree bool, images []string, trackID *int64) (*task.Task, error) {
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
		`INSERT INTO tasks (project_id, title, input, slug, workflow, branch_name, branch, target_branch, checkout_branch, status, priority, worktree, images, track_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, title, input, slug, workflow, branchName, branch, targetBranch, checkoutBranch, status, priority, worktreeInt, imagesJSON, trackID,
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

const taskColumns = `id, project_id, track_id, title, input, slug, workflow, status, priority, step_index, current_step, loop_iteration,
	branch_name, branch, target_branch, checkout_branch, worktree, worktree_path, worktree_detached, workdir, exit_code, error_message, context, images, commits, periodic_id,
	created_at, started_at, completed_at, updated_at, hibernated_at, not_before`

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

// GetClaimableTasks returns pending tasks whose blockers have all completed and
// whose not_before (if any) is at or before now.
func (db *DB) GetClaimableTasks(now time.Time) ([]*task.Task, error) {
	rows, err := db.sqlDB.Query(fmt.Sprintf(`
		SELECT %s FROM tasks
		WHERE status = ?
		AND (not_before IS NULL OR not_before <= ?)
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
	`, taskColumns), task.StatusPending, now.UTC(), task.StatusCompleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanTasks(rows)
}

func (db *DB) GetAllTasks() ([]*task.Task, error) {
	rows, err := db.sqlDB.Query(fmt.Sprintf(`SELECT %s FROM tasks ORDER BY (status = ?) DESC, id DESC`, taskColumns), task.StatusTmux)
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
	rows, err := db.sqlDB.Query(fmt.Sprintf(`SELECT %s FROM tasks WHERE project_id = ? ORDER BY (status = ?) DESC, id DESC`, taskColumns), projectID, task.StatusTmux)
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
	rows, err := db.sqlDB.Query(fmt.Sprintf(`SELECT %s FROM tasks WHERE project_id IN (SELECT id FROM projects WHERE name = ?) ORDER BY (status = ?) DESC, id DESC`, taskColumns), name, task.StatusTmux)
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

// GetActiveNonWorktreeTasks returns the project's worktree:false tasks that
// have been claimed and are not yet terminal (task.Status.IsActive()), oldest
// first. These are the tasks occupying the project root.
func (db *DB) GetActiveNonWorktreeTasks(projectID int64) ([]*task.Task, error) {
	rows, err := db.sqlDB.Query(fmt.Sprintf(`
		SELECT %s FROM tasks
		WHERE project_id = ? AND worktree = 0
		AND status NOT IN (?, ?, ?, ?, ?)
		ORDER BY id ASC
	`, taskColumns), projectID,
		task.StatusPending, task.StatusInit, task.StatusCompleted, task.StatusFailed, task.StatusMergeFailed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	active := tasks[:0]
	for _, t := range tasks {
		if t.Status.IsActive() {
			active = append(active, t)
		}
	}
	return active, nil
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
	return writeStatus(db.sqlDB, id, task.StatusRunning, []task.Status{task.StatusPending}, "started_at = ?", time.Now())
}

// statusExecer is the Exec half of *sql.DB and *sql.Tx.
type statusExecer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

// writeStatus is the single statement behind every guarded status write: it
// sets status (plus the extra SET fragment and its args) only while the row's
// current status is one of from, so the check and the write are one atomic
// step. It also clears hibernated_at, as does every other status-writing
// statement in this file: the marker only means anything while a task sits in
// tmux status, so leaving tmux by any route must drop it rather than leave a
// stale timestamp for the TUI to render. Reports whether a row was written.
func writeStatus(ex statusExecer, id int64, to task.Status, from []task.Status, set string, setArgs ...interface{}) (bool, error) {
	query := "UPDATE tasks SET status = ?, updated_at = ?, hibernated_at = NULL"
	args := []interface{}{to, time.Now()}
	if set != "" {
		query += ", " + set
		args = append(args, setArgs...)
	}
	query += " WHERE id = ? AND status IN (?" + strings.Repeat(", ?", len(from)-1) + ")"
	args = append(args, id)
	for _, f := range from {
		args = append(args, f)
	}
	result, err := ex.Exec(query, args...)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// guardedStatusWrite moves a task to `to` if task.CanTransition allows it from
// the task's current DB status, and otherwise returns (and logs) a
// *task.TransitionError. A missing task is a silent no-op.
func (db *DB) guardedStatusWrite(id int64, to task.Status, set string, setArgs ...interface{}) error {
	ok, err := writeStatus(db.sqlDB, id, to, task.AllowedFrom(to), set, setArgs...)
	if err != nil || ok {
		return err
	}
	return db.rejectTransition(id, "", to)
}

// rejectTransition builds the error for a status write that matched no row.
// from is the status the caller expected; when empty (or when the row has
// since moved on) the current DB status is reported instead.
func (db *DB) rejectTransition(id int64, from, to task.Status) error {
	var cur task.Status
	if err := db.sqlDB.QueryRow(`SELECT status FROM tasks WHERE id = ?`, id).Scan(&cur); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	if from == "" || cur != from {
		from = cur
	}
	log.Printf("Rejected status write for task #%d: %s -> %s", id, from, to)
	return &task.TransitionError{TaskID: id, From: from, To: to}
}

// statusSet returns the extra columns a write to status `to` stamps.
func statusSet(to task.Status) (string, []interface{}) {
	switch to {
	case task.StatusRunning:
		// started_at is write-once. A task re-enters "running" many times
		// during a single run — markSummarizingStep restores it after every
		// per-step summarization pass — and rewriting the column each time
		// reports the task as having started at its last summarizer round
		// trip instead of at its actual claim. ClaimTask sets the real start;
		// the retry paths NULL the column out to re-arm it.
		return "started_at = COALESCE(started_at, ?)", []interface{}{time.Now()}
	case task.StatusCompleted, task.StatusFailed:
		return "completed_at = ?", []interface{}{time.Now()}
	default:
		return "", nil
	}
}

// UpdateTaskStatus moves a task to status, provided the transition table in
// internal/task allows it from the task's current status; otherwise it
// returns a *task.TransitionError and leaves the row untouched.
func (db *DB) UpdateTaskStatus(id int64, status task.Status) error {
	set, args := statusSet(status)
	return db.guardedStatusWrite(id, status, set, args...)
}

// TransitionTaskStatus is the compare-and-set form of UpdateTaskStatus: it
// writes `to` only if the task is currently in `from` and the table allows
// from -> to. When two writers race the same transition, exactly one wins;
// the loser gets a *task.TransitionError carrying the status it lost to.
func (db *DB) TransitionTaskStatus(id int64, from, to task.Status) error {
	if !task.CanTransition(from, to) {
		log.Printf("Rejected status write for task #%d: %s -> %s", id, from, to)
		return &task.TransitionError{TaskID: id, From: from, To: to}
	}
	set, args := statusSet(to)
	ok, err := writeStatus(db.sqlDB, id, to, []task.Status{from}, set, args...)
	if err != nil || ok {
		return err
	}
	return db.rejectTransition(id, from, to)
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

// UpdateTaskError fails a task with errMsg, subject to the same transition
// check as UpdateTaskStatus.
func (db *DB) UpdateTaskError(id int64, errMsg string) error {
	return db.guardedStatusWrite(id, task.StatusFailed, "error_message = ?", errMsg)
}

// SetTaskWorkdir records the agent start directory (relative to the task's
// checkout) chosen at create time.
func (db *DB) SetTaskWorkdir(id int64, workdir string) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET workdir = ?, updated_at = ? WHERE id = ?",
		workdir, time.Now(), id,
	)
	return err
}

// SetTaskHibernatedAt stamps (or with a nil at, clears) the hibernation
// marker for a task. Deliberately does not touch status or updated_at:
// hibernation is invisible to the task lifecycle — the task stays exactly
// where it was, only its interactive session went away.
func (db *DB) SetTaskHibernatedAt(id int64, at *time.Time) error {
	_, err := db.sqlDB.Exec(`UPDATE tasks SET hibernated_at = ? WHERE id = ?`, at, id)
	return err
}

// UpdateTaskStatusError records an error message alongside a caller-supplied
// terminal status in a single write, so a poller can never observe the new
// status without its reason (or vice versa). Subject to the same transition
// check as UpdateTaskStatus.
func (db *DB) UpdateTaskStatusError(id int64, status task.Status, errMsg string) error {
	return db.guardedStatusWrite(id, status, "error_message = ?", errMsg)
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

func (db *DB) UpdateTaskInput(id int64, input string) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET input = ?, updated_at = ? WHERE id = ?",
		input, time.Now(), id,
	)
	return err
}

// UpdateTaskTrack attaches a task to a track, or detaches it when trackID is
// nil (the column is nullable — nil = trackless).
func (db *DB) UpdateTaskTrack(id int64, trackID *int64) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET track_id = ?, updated_at = ? WHERE id = ?",
		trackID, time.Now(), id,
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

// ResetTaskForRetry and the other ResetTaskFor* functions are the explicit
// force paths back to pending (retry, continue, startup recovery): they
// write regardless of the current status and bypass the transition table.
func (db *DB) ResetTaskForRetry(id int64) error {
	_, err := db.sqlDB.Exec(
		`UPDATE tasks SET status = ?, step_index = 0, current_step = NULL, loop_iteration = 0,
		 exit_code = NULL, error_message = NULL, started_at = NULL,
		 completed_at = NULL, hibernated_at = NULL, updated_at = ? WHERE id = ?`,
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
		 completed_at = NULL, hibernated_at = NULL, updated_at = ? WHERE id = ?`,
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
		 completed_at = NULL, hibernated_at = NULL, updated_at = ? WHERE id = ?`,
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
			`UPDATE tasks SET status = ?, workflow = ?, input = ?, step_index = 0, current_step = NULL, loop_iteration = 0,
			 exit_code = NULL, error_message = NULL, started_at = NULL,
			 completed_at = NULL, hibernated_at = NULL, updated_at = ? WHERE id = ?`,
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
		 completed_at = NULL, hibernated_at = NULL, updated_at = ? WHERE id = ?`,
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

// FailDependentTasks transitively fails every task blocked by blockerID, and
// every task blocked by those, and so on. It is the recovery for the deadlock
// a failed blocker otherwise creates: GetClaimableTasks only treats a
// `completed` blocker as satisfying a dependency, so a task whose blocker
// failed stays `pending` forever, never reaches a terminal status, and leaves
// any parent suspended on it (task_waits_on, released only once
// AllWaitsOnTerminal) suspended forever too. Nothing times that out.
//
// Only `failed` cascades. StatusMergeFailed is deliberately NOT a trigger:
// every workflow step of a merge-failed task succeeded and only finalization
// did not land, so the user can retry it into `completed` — which unblocks
// the dependents the ordinary way. Failing them on a state the user is about
// to recover would destroy work; the asymmetry is intentional, not an
// oversight.
//
// Each dependent is visited at most once, so a dependency cycle (which
// HasCircularDependency guards against on insert, but which an older row
// could still carry) terminates instead of looping. A dependent that is
// already terminal, or that the engine has already claimed and started
// (Status.IsActive), is left alone and not traversed through: only a task
// still waiting to start — `pending` or `init` — is stranded by the blocker's
// failure.
//
// Returns the IDs of the tasks it failed, in visit order.
func (db *DB) FailDependentTasks(blockerID int64) ([]int64, error) {
	tx, err := db.sqlDB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// The failed-only rule is enforced here rather than at the call sites, so
	// no future caller can accidentally cascade a merge-failed (or any other)
	// blocker.
	var blockerStatus task.Status
	if err := tx.QueryRow(`SELECT status FROM tasks WHERE id = ?`, blockerID).Scan(&blockerStatus); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if blockerStatus != task.StatusFailed {
		return nil, nil
	}

	type dependent struct {
		id     int64
		status task.Status
	}

	now := time.Now()
	visited := map[int64]bool{blockerID: true}
	queue := []int64{blockerID}
	var failed []int64

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		rows, err := tx.Query(
			`SELECT td.task_id, t.status FROM task_dependencies td
			 JOIN tasks t ON t.id = td.task_id
			 WHERE td.blocked_by = ?`, current)
		if err != nil {
			return nil, err
		}
		// Drain the cursor before issuing the UPDATEs below: the pool is
		// capped at one connection, so an Exec while this Query is still open
		// would deadlock.
		var deps []dependent
		for rows.Next() {
			var d dependent
			if err := rows.Scan(&d.id, &d.status); err != nil {
				rows.Close()
				return nil, err
			}
			deps = append(deps, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}

		for _, d := range deps {
			if visited[d.id] {
				continue
			}
			visited[d.id] = true
			if d.status.IsTerminal() || d.status.IsActive() {
				continue
			}
			if _, err := writeStatus(tx, d.id, task.StatusFailed, task.AllowedFrom(task.StatusFailed),
				"error_message = ?, completed_at = ?", fmt.Sprintf("blocker task #%d failed", current), now,
			); err != nil {
				return nil, err
			}
			failed = append(failed, d.id)
			queue = append(queue, d.id)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return failed, nil
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
	var periodicID sql.NullInt64
	var exitCode sql.NullInt64
	var worktreeInt int
	var worktreeDetached int
	var startedAt, completedAt sql.NullTime
	var updatedAt, hibernatedAt, notBefore sql.NullTime

	err := s.Scan(
		&t.ID, &projectID, &trackID, &title, &t.Input, &slug, &workflow, &t.Status, &priority,
		&t.StepIndex, &currentStep, &t.LoopIteration,
		&branchName, &branch, &targetBranch, &checkoutBranch, &worktreeInt, &worktreePath, &worktreeDetached, &t.Workdir, &exitCode, &errorMessage, &taskContext, &imagesJSON, &commitsJSON, &periodicID,
		&t.CreatedAt, &startedAt, &completedAt, &updatedAt, &hibernatedAt, &notBefore,
	)
	if err != nil {
		return nil, err
	}

	if periodicID.Valid {
		id := periodicID.Int64
		t.PeriodicID = &id
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
	if hibernatedAt.Valid {
		t.HibernatedAt = &hibernatedAt.Time
	}
	if notBefore.Valid {
		nb := notBefore.Time.UTC()
		t.NotBefore = &nb
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

// SetTaskPeriodicID links a task to the periodic definition that materialized
// it. Kept as a post-creation setter rather than threading a nullable arg
// through the long positional CreateTaskWithPriority.
func (db *DB) SetTaskPeriodicID(taskID, periodicID int64) error {
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET periodic_id = ?, updated_at = ? WHERE id = ?",
		periodicID, time.Now(), taskID,
	)
	return err
}

// SetTaskNotBefore sets (or with nil, clears) the earliest time the poller may
// claim the task. Stored in UTC so the lexical DATETIME comparison in
// GetClaimableTasks is consistent regardless of the caller's offset.
func (db *DB) SetTaskNotBefore(taskID int64, notBefore *time.Time) error {
	var v any
	if notBefore != nil {
		v = notBefore.UTC()
	}
	_, err := db.sqlDB.Exec(
		"UPDATE tasks SET not_before = ?, updated_at = ? WHERE id = ?",
		v, time.Now(), taskID,
	)
	return err
}

// SetPendingTaskNotBefore is SetTaskNotBefore guarded on status = pending in
// the same statement, so it cannot race the poller's claim. Returns false when
// the task is absent or not pending.
func (db *DB) SetPendingTaskNotBefore(taskID int64, notBefore *time.Time) (bool, error) {
	var v any
	if notBefore != nil {
		v = notBefore.UTC()
	}
	result, err := db.sqlDB.Exec(
		"UPDATE tasks SET not_before = ?, updated_at = ? WHERE id = ? AND status = ?",
		v, time.Now(), taskID, task.StatusPending,
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
