package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

func newProjectsTestServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s := NewServer(&config.Config{}, database)
	t.Cleanup(func() {
		s.manager.Shutdown(2 * time.Second)
		s.cancel()
	})
	return s, database
}

func removeProject(t *testing.T, s *Server, id int64) *Message {
	t.Helper()
	clientConn, serverConn := pipeForHandler(t)
	go s.handleRemoveProject(serverConn, RemoveProjectRequest{ProjectID: id})
	return readOneMessage(t, clientConn)
}

func listProjects(t *testing.T, s *Server) []ProjectInfo {
	t.Helper()
	clientConn, serverConn := pipeForHandler(t)
	go s.handleListProjects(serverConn)
	msg := readOneMessage(t, clientConn)
	if msg.Type != MsgListProjects {
		t.Fatalf("expected %s, got %s: %s", MsgListProjects, msg.Type, string(msg.Payload))
	}
	var resp ListProjectsResponse
	if err := json.Unmarshal(msg.Payload, &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Projects
}

func createProjectTask(t *testing.T, database taskStore, projectID int64, status task.Status) *task.Task {
	t.Helper()
	tk, err := database.CreateTask(projectID, "task", "in", "task", "default", "", status, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestHandleListProjects_ReportsPathAndCounts(t *testing.T) {
	s, database := newProjectsTestServer(t)
	live, err := database.GetOrCreateProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gonePath := t.TempDir()
	gone, err := database.GetOrCreateProject(gonePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gonePath); err != nil {
		t.Fatal(err)
	}
	createProjectTask(t, database, live.ID, task.StatusCompleted)
	createProjectTask(t, database, live.ID, task.StatusRunning)

	got := listProjects(t, s)
	want := []ProjectInfo{
		{ID: live.ID, Name: live.Name, Path: live.Path, PathExists: true, Tasks: 2, ActiveTasks: 1},
		{ID: gone.ID, Name: gone.Name, Path: gone.Path, PathExists: false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d projects, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("project %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestHandleRemoveProject_TerminalTasksAndWorktrees(t *testing.T) {
	s, pc, tk := setupIsolatedCleanupTask(t, false)
	failed := createProjectTask(t, s.database, tk.ProjectID, task.StatusFailed)

	msg := removeProject(t, s, tk.ProjectID)
	if msg.Type != MsgRemoveProject {
		t.Fatalf("expected %s, got %s: %s", MsgRemoveProject, msg.Type, string(msg.Payload))
	}
	var resp RemoveProjectResponse
	if err := json.Unmarshal(msg.Payload, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.TasksRemoved != 2 {
		t.Errorf("tasks removed = %d, want 2", resp.TasksRemoved)
	}
	if _, err := s.database.GetProject(tk.ProjectID); err == nil {
		t.Error("project row still present")
	}
	for _, id := range []int64{tk.ID, failed.ID} {
		if _, err := s.database.GetTask(id); err == nil {
			t.Errorf("task #%d still present", id)
		}
	}
	s.projectsMu.RLock()
	_, cached := s.projects[tk.ProjectID]
	s.projectsMu.RUnlock()
	if cached {
		t.Error("project context still cached")
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		left := branchWorktreesGone(t, pc, tk, false)
		if _, err := os.Stat(tk.WorktreePath); !os.IsNotExist(err) {
			left += "; task worktree on disk"
		}
		if left == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background cleanup left: %s", left)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestHandleRemoveProject_RefusesWithActiveTask(t *testing.T) {
	s, database := newProjectsTestServer(t)
	proj, err := database.GetOrCreateProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	done := createProjectTask(t, database, proj.ID, task.StatusCompleted)
	blocked := createProjectTask(t, database, proj.ID, task.StatusMergeBlocked)

	msg := removeProject(t, s, proj.ID)
	if msg.Type != MsgError {
		t.Fatalf("expected MsgError, got %s: %s", msg.Type, string(msg.Payload))
	}
	var resp ErrorResponse
	if err := json.Unmarshal(msg.Payload, &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Message, fmt.Sprintf("#%d ", blocked.ID)) || !strings.Contains(resp.Message, string(task.StatusMergeBlocked)) {
		t.Errorf("error does not name the active task: %s", resp.Message)
	}
	if strings.Contains(resp.Message, fmt.Sprintf("#%d ", done.ID)) {
		t.Errorf("error names a terminal task: %s", resp.Message)
	}
	if _, err := database.GetProject(proj.ID); err != nil {
		t.Errorf("project removed despite refusal: %v", err)
	}
	if _, err := database.GetTask(done.ID); err != nil {
		t.Errorf("terminal task removed despite refusal: %v", err)
	}
}

func TestHandleRemoveProject_MissingPath(t *testing.T) {
	s, database := newProjectsTestServer(t)
	path := t.TempDir()
	proj, err := database.GetOrCreateProject(path)
	if err != nil {
		t.Fatal(err)
	}
	tk := createProjectTask(t, database, proj.ID, task.StatusCompleted)
	if _, err := database.UpsertPeriodicDef(proj.ID, "nightly", "@daily", "", "default", "", "", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}

	msg := removeProject(t, s, proj.ID)
	if msg.Type != MsgRemoveProject {
		t.Fatalf("expected %s, got %s: %s", MsgRemoveProject, msg.Type, string(msg.Payload))
	}
	if _, err := database.GetProject(proj.ID); err == nil {
		t.Error("project row still present")
	}
	if _, err := database.GetTask(tk.ID); err == nil {
		t.Error("task still present")
	}
	if defs, err := database.ListPeriodicsForProject(proj.ID); err != nil || len(defs) != 0 {
		t.Errorf("routine rows left: %v (err %v)", defs, err)
	}
	if got := listProjects(t, s); len(got) != 0 {
		t.Errorf("projects still listed: %+v", got)
	}
}

func TestHandleRemoveProject_UnknownProject(t *testing.T) {
	s, _ := newProjectsTestServer(t)
	msg := removeProject(t, s, 99)
	if msg.Type != MsgError {
		t.Fatalf("expected MsgError, got %s: %s", msg.Type, string(msg.Payload))
	}
}
