package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/task"
)

type Project struct {
	ID                int64
	Path              string
	Name              string
	DefaultPriority   task.Priority
	DefaultWorktree   bool
	CreatedAt         time.Time
	DefaultBranchMode int
	DefaultWorkflow   string
}

// GetOrCreateProject returns the project for the given path, creating it if needed.
func (db *DB) GetOrCreateProject(projectPath string) (*Project, error) {
	// Normalize to the symlink-resolved path so aliases of one directory
	// share a row.
	canon, err := config.CanonicalPath(projectPath)
	if err != nil {
		canon = projectPath
	}

	p, err := db.GetProjectByPath(canon)
	if err == nil {
		return p, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to query project: %w", err)
	}

	// A row stored before paths were canonicalized may hold an alias of canon;
	// the lowest id wins.
	projects, err := db.ListProjects()
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}
	for _, row := range projects {
		if rowCanon, err := config.CanonicalPath(row.Path); err == nil && rowCanon == canon {
			return row, nil
		}
	}

	// Create new project — derive canonical name from path. Must match the
	// name TUI/clients use to look up tasks, otherwise filtering will silently
	// return nothing (see ProjectNameFromPath docstring).
	name := config.ProjectNameFromPath(canon)
	result, err := db.sqlDB.Exec(
		`INSERT INTO projects (path, name) VALUES (?, ?)`,
		canon, name,
	)
	if err != nil {
		// Race condition: another goroutine may have created it
		p, err2 := db.GetProjectByPath(canon)
		if err2 == nil {
			return p, nil
		}
		return nil, fmt.Errorf("failed to create project: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &Project{
		ID:              id,
		Path:            canon,
		Name:            name,
		DefaultPriority: task.PriorityMedium,
		DefaultWorktree: true,
		CreatedAt:       time.Now(),
	}, nil
}

// projectScanner is an interface satisfied by both *sql.Row and *sql.Rows.
type projectScanner interface {
	Scan(dest ...any) error
}

// scanProject scans a single project row from a query result.
func scanProject(s projectScanner) (*Project, error) {
	var p Project
	var defaultPriority sql.NullString
	if err := s.Scan(&p.ID, &p.Path, &p.Name, &defaultPriority, &p.DefaultWorktree, &p.CreatedAt, &p.DefaultBranchMode, &p.DefaultWorkflow); err != nil {
		return nil, err
	}
	if defaultPriority.Valid {
		p.DefaultPriority = task.Priority(defaultPriority.String)
	} else {
		p.DefaultPriority = task.PriorityMedium
	}
	return &p, nil
}

// scanProjects scans multiple project rows from a query result.
func scanProjects(rows *sql.Rows) ([]*Project, error) {
	var projects []*Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// GetProjectByPath finds a project by its filesystem path.
func (db *DB) GetProjectByPath(path string) (*Project, error) {
	return scanProject(db.sqlDB.QueryRow(
		`SELECT id, path, name, default_priority, default_worktree, created_at, default_branch_mode, default_workflow FROM projects WHERE path = ?`, path,
	))
}

// GetProject returns a project by ID.
func (db *DB) GetProject(id int64) (*Project, error) {
	return scanProject(db.sqlDB.QueryRow(
		`SELECT id, path, name, default_priority, default_worktree, created_at, default_branch_mode, default_workflow FROM projects WHERE id = ?`, id,
	))
}

// GetProjectsByName returns all projects matching the given name (basename).
func (db *DB) GetProjectsByName(name string) ([]*Project, error) {
	rows, err := db.sqlDB.Query(`SELECT id, path, name, default_priority, default_worktree, created_at, default_branch_mode, default_workflow FROM projects WHERE name = ? ORDER BY id ASC`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanProjects(rows)
}

// ListProjects returns all registered projects.
func (db *DB) ListProjects() ([]*Project, error) {
	rows, err := db.sqlDB.Query(`SELECT id, path, name, default_priority, default_worktree, created_at, default_branch_mode, default_workflow FROM projects ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanProjects(rows)
}

// UpdateProjectDefaultWorktree updates the default worktree preference for a project.
func (db *DB) UpdateProjectDefaultWorktree(id int64, worktree bool) error {
	_, err := db.sqlDB.Exec(`UPDATE projects SET default_worktree = ? WHERE id = ?`, worktree, id)
	return err
}

// UpdateProjectDefaultBranchMode updates the default branch mode for a project.
func (db *DB) UpdateProjectDefaultBranchMode(id int64, mode int) error {
	_, err := db.sqlDB.Exec(`UPDATE projects SET default_branch_mode = ? WHERE id = ?`, mode, id)
	return err
}

// UpdateProjectDefaultWorkflow updates the default workflow for a project.
func (db *DB) UpdateProjectDefaultWorkflow(id int64, workflow string) error {
	_, err := db.sqlDB.Exec(`UPDATE projects SET default_workflow = ? WHERE id = ?`, workflow, id)
	return err
}

// UpdateProjectDefaults updates worktree, branch mode, and workflow defaults for a project in one operation.
func (db *DB) UpdateProjectDefaults(id int64, worktree bool, branchMode int, workflow string) error {
	_, err := db.sqlDB.Exec(`UPDATE projects SET default_worktree = ?, default_branch_mode = ?, default_workflow = ? WHERE id = ?`, worktree, branchMode, workflow, id)
	return err
}

// ProjectTaskCounts is a project's task total and how many of those are not
// yet terminal.
type ProjectTaskCounts struct {
	Total  int
	Active int
}

// CountTasksByProject returns task counts keyed by project ID. Projects with
// no tasks are absent from the map.
func (db *DB) CountTasksByProject() (map[int64]ProjectTaskCounts, error) {
	rows, err := db.sqlDB.Query(`SELECT project_id, status, COUNT(*) FROM tasks GROUP BY project_id, status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[int64]ProjectTaskCounts)
	for rows.Next() {
		var projectID int64
		var status string
		var n int
		if err := rows.Scan(&projectID, &status, &n); err != nil {
			return nil, err
		}
		c := counts[projectID]
		c.Total += n
		if !task.Status(status).IsTerminal() {
			c.Active += n
		}
		counts[projectID] = c
	}
	return counts, rows.Err()
}

// ActiveTasksError reports the non-terminal tasks that block a project removal.
type ActiveTasksError struct {
	TaskIDs []int64
}

func (e *ActiveTasksError) Error() string {
	ids := make([]string, len(e.TaskIDs))
	for i, id := range e.TaskIDs {
		ids[i] = fmt.Sprintf("#%d", id)
	}
	return fmt.Sprintf("project has %d active task(s): %s", len(e.TaskIDs), strings.Join(ids, ", "))
}

// DeleteProject removes a project and everything keyed by it in one
// transaction: its tasks (through deleteTaskRows), routine definitions and
// tracks. Tasks of other projects attached to one of its tracks are detached.
// It returns *ActiveTasksError, deleting nothing, while any of the project's
// tasks is not terminal, and sql.ErrNoRows when the project does not exist.
func (db *DB) DeleteProject(id int64) error {
	tx, err := db.sqlDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM projects WHERE id = ?`, id).Scan(&exists); err != nil {
		return err
	}

	rows, err := tx.Query(`SELECT id, status FROM tasks WHERE project_id = ? ORDER BY id`, id)
	if err != nil {
		return err
	}
	var taskIDs, active []int64
	for rows.Next() {
		var taskID int64
		var status string
		if err := rows.Scan(&taskID, &status); err != nil {
			rows.Close()
			return err
		}
		taskIDs = append(taskIDs, taskID)
		if !task.Status(status).IsTerminal() {
			active = append(active, taskID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(active) > 0 {
		return &ActiveTasksError{TaskIDs: active}
	}

	// last_task_id has no ON DELETE action, so the routines' run pointers
	// must let go of the tasks before they can be deleted.
	if _, err := tx.Exec(`UPDATE periodic_definitions SET last_task_id = NULL WHERE last_task_id IN (SELECT id FROM tasks WHERE project_id = ?)`, id); err != nil {
		return err
	}
	for _, taskID := range taskIDs {
		if err := deleteTaskRows(tx, taskID); err != nil {
			return fmt.Errorf("failed to delete task #%d: %w", taskID, err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM periodic_definitions WHERE project_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE tasks SET track_id = NULL WHERE track_id IN (SELECT id FROM tracks WHERE project_id = ?)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM tracks WHERE project_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM projects WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
