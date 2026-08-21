package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
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
	// Normalize path
	absPath, err := filepath.Abs(projectPath)
	if err != nil {
		absPath = projectPath
	}

	// Try to find existing
	p, err := db.GetProjectByPath(absPath)
	if err == nil {
		return p, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to query project: %w", err)
	}

	// Create new project — derive canonical name from path. Must match the
	// name TUI/clients use to look up tasks, otherwise filtering will silently
	// return nothing (see ProjectNameFromPath docstring).
	name := config.ProjectNameFromPath(absPath)
	result, err := db.sqlDB.Exec(
		`INSERT INTO projects (path, name) VALUES (?, ?)`,
		absPath, name,
	)
	if err != nil {
		// Race condition: another goroutine may have created it
		p, err2 := db.GetProjectByPath(absPath)
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
		Path:            absPath,
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
