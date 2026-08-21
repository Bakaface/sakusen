package db

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
)

func TestGetOrCreateProject(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Create a project
	proj, err := database.GetOrCreateProject("/home/user/myproject")
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	if proj.ID == 0 {
		t.Error("expected non-zero project ID")
	}
	if proj.Path != "/home/user/myproject" {
		t.Errorf("expected path '/home/user/myproject', got '%s'", proj.Path)
	}
	if proj.Name != "myproject" {
		t.Errorf("expected name 'myproject', got '%s'", proj.Name)
	}

	// Get the same project again (should return existing)
	proj2, err := database.GetOrCreateProject("/home/user/myproject")
	if err != nil {
		t.Fatalf("failed to get existing project: %v", err)
	}
	if proj2.ID != proj.ID {
		t.Errorf("expected same ID %d, got %d", proj.ID, proj2.ID)
	}
}

func TestGetOrCreateProject_NormalizesPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Use current directory as a relative path that can be resolved
	cwd, _ := os.Getwd()
	proj, err := database.GetOrCreateProject(cwd)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	if proj.Path != cwd {
		t.Errorf("expected absolute path '%s', got '%s'", cwd, proj.Path)
	}
}

func TestGetProjectByPath_NotFound(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	_, err = database.GetProjectByPath("/nonexistent/path")
	if err == nil {
		t.Error("expected error for non-existent project")
	}
}

func TestListProjects(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Create two projects
	_, err = database.GetOrCreateProject("/home/user/project-a")
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.GetOrCreateProject("/home/user/project-b")
	if err != nil {
		t.Fatal(err)
	}

	projects, err := database.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Errorf("expected 2 projects, got %d", len(projects))
	}
}

func TestGetTasksByProject(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Create two projects
	projA, err := database.GetOrCreateProject("/home/user/project-a")
	if err != nil {
		t.Fatal(err)
	}
	projB, err := database.GetOrCreateProject("/home/user/project-b")
	if err != nil {
		t.Fatal(err)
	}

	// Create tasks for each project
	_, err = database.CreateTask(projA.ID, "Task A1", "Description A1", "task-a1", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateTask(projA.ID, "Task A2", "Description A2", "task-a2", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateTask(projB.ID, "Task B1", "Description B1", "task-b1", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Get tasks for project A
	tasksA, err := database.GetTasksByProject(projA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasksA) != 2 {
		t.Errorf("expected 2 tasks for project A, got %d", len(tasksA))
	}
	for _, task := range tasksA {
		if task.ProjectID != projA.ID {
			t.Errorf("expected project_id %d, got %d", projA.ID, task.ProjectID)
		}
	}

	// Get tasks for project B
	tasksB, err := database.GetTasksByProject(projB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasksB) != 1 {
		t.Errorf("expected 1 task for project B, got %d", len(tasksB))
	}

	// Get all tasks
	allTasks, err := database.GetAllTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(allTasks) != 3 {
		t.Errorf("expected 3 total tasks, got %d", len(allTasks))
	}
}

func TestCreateTask_WithProjectID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	proj, err := database.GetOrCreateProject("/home/user/myproject")
	if err != nil {
		t.Fatal(err)
	}

	task, err := database.CreateTask(proj.ID, "Test", "Test desc", "test", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}
	if task.ProjectID != proj.ID {
		t.Errorf("expected project_id %d, got %d", proj.ID, task.ProjectID)
	}
}

func TestGetProjectsByName(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Create projects with different paths but same basename
	_, err = database.GetOrCreateProject("/home/user/work/myapp")
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.GetOrCreateProject("/home/user/personal/myapp")
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.GetOrCreateProject("/home/user/other-project")
	if err != nil {
		t.Fatal(err)
	}

	// Query by name "myapp" should return both
	projects, err := database.GetProjectsByName("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Errorf("expected 2 projects named 'myapp', got %d", len(projects))
	}
	for _, p := range projects {
		if p.Name != "myapp" {
			t.Errorf("expected name 'myapp', got '%s'", p.Name)
		}
	}

	// Query by name "other-project" should return one
	projects, err = database.GetProjectsByName("other-project")
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Errorf("expected 1 project named 'other-project', got %d", len(projects))
	}

	// Query by non-existent name should return empty
	projects, err = database.GetProjectsByName("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Errorf("expected 0 projects named 'nonexistent', got %d", len(projects))
	}
}

func TestGetTasksByProjectName(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Create projects: two with same name, one different
	projA, err := database.GetOrCreateProject("/home/user/work/myapp")
	if err != nil {
		t.Fatal(err)
	}
	projB, err := database.GetOrCreateProject("/home/user/personal/myapp")
	if err != nil {
		t.Fatal(err)
	}
	projC, err := database.GetOrCreateProject("/home/user/other-project")
	if err != nil {
		t.Fatal(err)
	}

	// Create tasks for each project
	_, err = database.CreateTask(projA.ID, "Task A1", "Desc A1", "task-a1", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateTask(projB.ID, "Task B1", "Desc B1", "task-b1", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateTask(projC.ID, "Task C1", "Desc C1", "task-c1", "", "", "pending", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Filter by "myapp" should return tasks from both myapp projects
	tasks, err := database.GetTasksByProjectName("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks for 'myapp', got %d", len(tasks))
	}

	// Filter by "other-project" should return 1 task
	tasks, err = database.GetTasksByProjectName("other-project")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task for 'other-project', got %d", len(tasks))
	}

	// Filter by non-existent name should return empty
	tasks, err = database.GetTasksByProjectName("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks for 'nonexistent', got %d", len(tasks))
	}
}

// Regression: dot-prefixed project directories (e.g. ".pai") were stored under
// a sanitized name ("_pai") but the TUI looked them up by the raw basename
// (".pai") → tasks vanished from the list. The round-trip below ensures the
// DB-write helper agrees with the canonical name derivation now used by
// callers (config.ProjectNameFromPath).
func TestGetTasksByProjectName_DotPrefixedDirectory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repoPath := "/home/user/.pai"
	proj, err := database.GetOrCreateProject(repoPath)
	if err != nil {
		t.Fatal(err)
	}

	// Sanity: stored name matches the helper callers must use.
	wantName := config.ProjectNameFromPath(repoPath)
	if proj.Name != wantName {
		t.Fatalf("stored project name %q != ProjectNameFromPath(%q)=%q",
			proj.Name, repoPath, wantName)
	}

	if _, err := database.CreateTask(proj.ID, "Task 1", "desc", "task-1", "", "", "pending", nil); err != nil {
		t.Fatal(err)
	}

	tasks, err := database.GetTasksByProjectName(wantName)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task for dot-prefixed project lookup by %q, got %d",
			wantName, len(tasks))
	}
}

func TestProjectDefaultWorktree(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// New project should default to worktree=true
	proj, err := database.GetOrCreateProject("/home/user/myproject")
	if err != nil {
		t.Fatal(err)
	}
	if !proj.DefaultWorktree {
		t.Error("expected default worktree to be true for new project")
	}

	// Update to false
	if err := database.UpdateProjectDefaultWorktree(proj.ID, false); err != nil {
		t.Fatalf("failed to update default worktree: %v", err)
	}

	// Re-fetch and verify
	proj2, err := database.GetProject(proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if proj2.DefaultWorktree {
		t.Error("expected default worktree to be false after update")
	}

	// GetOrCreateProject should also return the updated value
	proj3, err := database.GetOrCreateProject("/home/user/myproject")
	if err != nil {
		t.Fatal(err)
	}
	if proj3.DefaultWorktree {
		t.Error("expected default worktree to be false from GetOrCreateProject")
	}

	// Update back to true
	if err := database.UpdateProjectDefaultWorktree(proj.ID, true); err != nil {
		t.Fatalf("failed to update default worktree: %v", err)
	}
	proj4, err := database.GetProject(proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !proj4.DefaultWorktree {
		t.Error("expected default worktree to be true after re-enabling")
	}
}

func TestProjectDefaultWorktree_ListAndGetByName(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Create two projects
	proj1, err := database.GetOrCreateProject("/home/user/project-a")
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.GetOrCreateProject("/home/user/project-b")
	if err != nil {
		t.Fatal(err)
	}

	// Disable worktree for first project only
	if err := database.UpdateProjectDefaultWorktree(proj1.ID, false); err != nil {
		t.Fatal(err)
	}

	// ListProjects should reflect the different settings
	projects, err := database.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
	if projects[0].DefaultWorktree {
		t.Error("expected project-a default_worktree=false")
	}
	if !projects[1].DefaultWorktree {
		t.Error("expected project-b default_worktree=true")
	}

	// GetProjectsByName should also work
	byName, err := database.GetProjectsByName("project-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(byName) != 1 {
		t.Fatalf("expected 1 project, got %d", len(byName))
	}
	if byName[0].DefaultWorktree {
		t.Error("expected project-a default_worktree=false via GetProjectsByName")
	}
}

func TestMigration_FreshDBHasProjectsTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Fresh DB should be able to create projects (table exists from schema)
	proj, err := database.GetOrCreateProject("/home/user/test-project")
	if err != nil {
		t.Fatalf("expected to create project in fresh DB: %v", err)
	}
	if proj.ID == 0 {
		t.Error("expected non-zero project ID")
	}
}

func TestProjectDefaultBranchMode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// New project should default to branch mode 0
	proj, err := database.GetOrCreateProject("/home/user/myproject")
	if err != nil {
		t.Fatal(err)
	}
	if proj.DefaultBranchMode != 0 {
		t.Errorf("expected default branch mode 0, got %d", proj.DefaultBranchMode)
	}

	// Update to 1
	if err := database.UpdateProjectDefaultBranchMode(proj.ID, 1); err != nil {
		t.Fatalf("failed to update default branch mode: %v", err)
	}

	// Re-fetch and verify
	proj2, err := database.GetProject(proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if proj2.DefaultBranchMode != 1 {
		t.Errorf("expected default branch mode 1, got %d", proj2.DefaultBranchMode)
	}

	// Update back to 0
	if err := database.UpdateProjectDefaultBranchMode(proj.ID, 0); err != nil {
		t.Fatalf("failed to update default branch mode: %v", err)
	}
	proj3, err := database.GetProject(proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if proj3.DefaultBranchMode != 0 {
		t.Errorf("expected default branch mode 0 after reset, got %d", proj3.DefaultBranchMode)
	}
}

func TestProjectDefaultWorkflow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// New project should default to empty workflow
	proj, err := database.GetOrCreateProject("/home/user/myproject")
	if err != nil {
		t.Fatal(err)
	}
	if proj.DefaultWorkflow != "" {
		t.Errorf("expected empty default workflow, got %q", proj.DefaultWorkflow)
	}

	// Update to a workflow name
	if err := database.UpdateProjectDefaultWorkflow(proj.ID, "feature"); err != nil {
		t.Fatalf("failed to update default workflow: %v", err)
	}

	// Re-fetch and verify
	proj2, err := database.GetProject(proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if proj2.DefaultWorkflow != "feature" {
		t.Errorf("expected default workflow 'feature', got %q", proj2.DefaultWorkflow)
	}

	// Update to empty
	if err := database.UpdateProjectDefaultWorkflow(proj.ID, ""); err != nil {
		t.Fatalf("failed to clear default workflow: %v", err)
	}
	proj3, err := database.GetProject(proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if proj3.DefaultWorkflow != "" {
		t.Errorf("expected empty default workflow after clear, got %q", proj3.DefaultWorkflow)
	}
}

func TestProjectDefaults_UpdateAll(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	proj, err := database.GetOrCreateProject("/home/user/myproject")
	if err != nil {
		t.Fatal(err)
	}

	// Update all defaults at once
	if err := database.UpdateProjectDefaults(proj.ID, false, 1, "bugfix"); err != nil {
		t.Fatalf("failed to update project defaults: %v", err)
	}

	// Re-fetch and verify all fields
	proj2, err := database.GetProject(proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if proj2.DefaultWorktree {
		t.Error("expected default_worktree=false")
	}
	if proj2.DefaultBranchMode != 1 {
		t.Errorf("expected default_branch_mode=1, got %d", proj2.DefaultBranchMode)
	}
	if proj2.DefaultWorkflow != "bugfix" {
		t.Errorf("expected default_workflow='bugfix', got %q", proj2.DefaultWorkflow)
	}

	// Update again with different values
	if err := database.UpdateProjectDefaults(proj.ID, true, 0, ""); err != nil {
		t.Fatalf("failed to update project defaults: %v", err)
	}
	proj3, err := database.GetProject(proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !proj3.DefaultWorktree {
		t.Error("expected default_worktree=true after second update")
	}
	if proj3.DefaultBranchMode != 0 {
		t.Errorf("expected default_branch_mode=0, got %d", proj3.DefaultBranchMode)
	}
	if proj3.DefaultWorkflow != "" {
		t.Errorf("expected empty default_workflow, got %q", proj3.DefaultWorkflow)
	}
}
