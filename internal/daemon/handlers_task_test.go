package daemon

import (
	"bufio"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/db"
	"github.com/Bakaface/sortie/internal/task"
)

// setupServerWithProject creates an in-memory DB, a project, and returns the
// Server and project ID for ref-validation tests.
func setupServerWithProject(t *testing.T) (*Server, int64) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	proj, err := database.GetOrCreateProject("/tmp/sortie-test")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	s := NewServer(cfg, database)
	return s, proj.ID
}

func TestValidateTaskRefs_NoRefs(t *testing.T) {
	s, projID := setupServerWithProject(t)
	got, err := s.validateTaskRefs("plain description with no refs", projID, 0, "input")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil auto-blocked-by, got %+v", got)
	}
}

func TestValidateTaskRefs_MissingTaskID(t *testing.T) {
	s, projID := setupServerWithProject(t)
	_, err := s.validateTaskRefs("see {{tasks.99.title}}", projID, 0, "input")
	if err == nil || !strings.Contains(err.Error(), "#99") {
		t.Fatalf("expected error mentioning task #99, got %v", err)
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error should mention missing task: %v", err)
	}
}

func TestValidateTaskRefs_DifferentProject(t *testing.T) {
	s, projID := setupServerWithProject(t)
	// Create a task in a different project.
	other, err := s.database.GetOrCreateProject("/tmp/sortie-other")
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.database.CreateTask(other.ID, "other proj task", "", "slug", "default", "main", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.validateTaskRefs("ref={{tasks."+itoa(tk.ID)+".title}}", projID, 0, "input")
	if err == nil || !strings.Contains(err.Error(), "another project") {
		t.Fatalf("expected cross-project error, got %v", err)
	}
}

func TestValidateTaskRefs_FailedDep(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tk, err := s.database.CreateTask(projID, "failed dep", "", "slug", "default", "main", task.StatusFailed, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.validateTaskRefs("dep={{tasks."+itoa(tk.ID)+".title}}", projID, 0, "input")
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("expected failed-dep error, got %v", err)
	}
}

// A merge-failed dep is terminal but never becomes `completed`, so auto-adding
// it as a blocker would leave the referencing task unclaimable forever
// (GetClaimableTasks only clears deps on `completed`). Reject it up front.
func TestValidateTaskRefs_MergeFailedDep(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tk, err := s.database.CreateTask(projID, "merge-failed dep", "", "slug", "default", "main", task.StatusMergeFailed, nil)
	if err != nil {
		t.Fatal(err)
	}

	blocked, err := s.validateTaskRefs("dep={{tasks."+itoa(tk.ID)+".title}}", projID, 0, "description")
	if err == nil {
		t.Fatalf("expected merge-failed-dep error, got auto-blocked-by %+v", blocked)
	}
	if !strings.Contains(err.Error(), "merge-failed") {
		t.Errorf("error should name the merge-failed status: %v", err)
	}
}

func TestValidateTaskRefs_UnsupportedField(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tk, err := s.database.CreateTask(projID, "ok", "", "slug", "default", "main", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.validateTaskRefs("x={{tasks."+itoa(tk.ID)+".slug}}", projID, 0, "input")
	if err == nil || !strings.Contains(err.Error(), "slug") {
		t.Fatalf("expected unsupported-field error, got %v", err)
	}
	if !strings.Contains(err.Error(), "supported:") {
		t.Errorf("error should list supported fields: %v", err)
	}
}

func TestValidateTaskRefs_ActiveDepAutoBlocks(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tk1, err := s.database.CreateTask(projID, "active a", "", "a", "default", "main", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}
	tk2, err := s.database.CreateTask(projID, "active b", "", "b", "default", "main", task.StatusRunning, nil)
	if err != nil {
		t.Fatal(err)
	}

	auto, err := s.validateTaskRefs(
		"foo {{tasks."+itoa(tk1.ID)+".title}} bar {{tasks."+itoa(tk2.ID)+".branch}}",
		projID,
		0,
		"input",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auto) != 2 {
		t.Fatalf("expected 2 auto-blockers, got %d: %v", len(auto), auto)
	}
	// Dedup: repeating the same id should still only produce one entry.
	auto2, err := s.validateTaskRefs(
		"a {{tasks."+itoa(tk1.ID)+".title}} a again {{tasks."+itoa(tk1.ID)+".input}}",
		projID,
		0,
		"input",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auto2) != 1 || auto2[0] != tk1.ID {
		t.Errorf("expected single deduped id, got %v", auto2)
	}
}

func TestValidateTaskRefs_CompletedDepNoAutoBlock(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tk, err := s.database.CreateTask(projID, "done", "", "slug", "default", "main", task.StatusCompleted, nil)
	if err != nil {
		t.Fatal(err)
	}

	auto, err := s.validateTaskRefs("x={{tasks."+itoa(tk.ID)+".title}}", projID, 0, "input")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auto) != 0 {
		t.Errorf("completed dep should not auto-block, got %v", auto)
	}
}

func TestMergeBlockedBy(t *testing.T) {
	cases := []struct {
		name     string
		explicit []int64
		auto     []int64
		want     []int64
	}{
		{"both empty", nil, nil, nil},
		{"only explicit", []int64{3, 1, 2}, nil, []int64{3, 1, 2}},
		{"only auto", nil, []int64{5, 4}, []int64{5, 4}},
		{"no overlap", []int64{1, 2}, []int64{3, 4}, []int64{1, 2, 3, 4}},
		{"overlap deduped", []int64{1, 2, 3}, []int64{2, 4, 1}, []int64{1, 2, 3, 4}},
		{"explicit dup removed", []int64{1, 1, 2}, nil, []int64{1, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeBlockedBy(tc.explicit, tc.auto)
			if !equalInts(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func equalInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// itoa is a small convenience wrapper around strconv.FormatInt to keep
// per-line clutter low in the table-driven tests above.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// --- Edit handler tests -----------------------------------------------------

func TestHandleUpdateField_DescriptionAutoAddsDep(t *testing.T) {
	s, projID := setupServerWithProject(t)
	host, err := s.database.CreateTask(projID, "host", "old description", "host", "default", "main", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.database.CreateTask(projID, "dep", "", "dep", "default", "main", task.StatusRunning, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Run the validation+merge logic the handler performs.
	newValue := "now references {{tasks." + itoa(dep.ID) + ".title}}"
	auto, err := s.validateTaskRefs(newValue, projID, host.ID, "input")
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	if err := s.database.UpdateTaskInput(host.ID, newValue); err != nil {
		t.Fatal(err)
	}
	for _, d := range auto {
		if err := s.database.AddTaskDependency(host.ID, d); err != nil {
			t.Fatal(err)
		}
	}

	updated, err := s.database.GetTask(host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.BlockedBy) != 1 || updated.BlockedBy[0] != dep.ID {
		t.Errorf("expected BlockedBy=[%d], got %v", dep.ID, updated.BlockedBy)
	}
}

func TestHandleUpdateField_RejectsBadField(t *testing.T) {
	s, projID := setupServerWithProject(t)
	host, err := s.database.CreateTask(projID, "host", "ok", "host", "default", "main", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.database.CreateTask(projID, "other", "", "other", "default", "main", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}

	bad := "see {{tasks." + itoa(other.ID) + ".slug}}"
	_, err = s.validateTaskRefs(bad, projID, host.ID, "input")
	if err == nil {
		t.Fatal("expected error for unsupported field")
	}
	// Original description should remain unchanged because validation runs
	// before mutation in the handler.
	got, err := s.database.GetTask(host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Input != "ok" {
		t.Errorf("description should be unchanged after rejection, got %q", got.Input)
	}
}

// A task's description may legitimately reference its own ID — runtime lookup
// resolves the value — but the validator must never auto-add a self-blocking
// edge. The claim filter would otherwise leave the task blocked forever.
func TestValidateTaskRefs_SelfReferenceNotBlocker(t *testing.T) {
	s, projID := setupServerWithProject(t)
	self, err := s.database.CreateTask(projID, "host", "old", "host", "default", "main", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}

	auto, err := s.validateTaskRefs(
		"refers to itself {{tasks."+itoa(self.ID)+".title}}",
		projID,
		self.ID,
		"input",
	)
	if err != nil {
		t.Fatalf("self-ref should not error: %v", err)
	}
	if len(auto) != 0 {
		t.Errorf("self-ref must not become an auto-blocker, got %v", auto)
	}
}

// Status init is transient during task creation; it should be treated as
// active and auto-add the referenced task to BlockedBy.
func TestValidateTaskRefs_InitStatusAutoBlocks(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tk, err := s.database.CreateTask(projID, "initializing", "", "init", "default", "main", task.StatusInit, nil)
	if err != nil {
		t.Fatal(err)
	}

	auto, err := s.validateTaskRefs(
		"refs {{tasks."+itoa(tk.ID)+".title}}",
		projID,
		0,
		"input",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auto) != 1 || auto[0] != tk.ID {
		t.Errorf("init-status dep should be auto-blocker, got %v", auto)
	}
}

// --- Pin-fallback tests for createTaskFromRequest --------------------------

// setupServerWithPinnedWorkflow creates a server with an in-memory DB, a
// project registered at projectPath, and injects a pre-built projectContext
// carrying a config that contains the given workflow. This avoids loading real
// global config files (which differ per developer machine) while still exercising
// the full getProjectContext → createTaskFromRequest pin-fallback path.
func setupServerWithPinnedWorkflow(t *testing.T, projectPath string, wf config.WorkflowConfig) (*Server, int64) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	proj, err := database.GetOrCreateProject(projectPath)
	if err != nil {
		t.Fatal(err)
	}

	// Build a config that contains the pinned workflow.
	pinnedCfg := &config.Config{
		Workflows: []config.WorkflowConfig{wf},
	}

	s := NewServer(&config.Config{}, database)

	// Directly inject the projectContext so getProjectContext returns our config
	// without loading any file on disk or the global ~/.sortie.yml.
	s.projectsMu.Lock()
	s.projects[proj.ID] = &projectContext{
		cfg:      pinnedCfg,
		repoRoot: projectPath,
		// Match the live fingerprint so the tracks-workflows freshness check
		// doesn't evict this hand-primed entry.
		tracksFingerprint: tracksFingerprint(projectPath),
	}
	s.projectsMu.Unlock()

	return s, proj.ID
}

// TestHandleDeleteTask_DeletesRowAndRespondsOK guards the delete handler's
// ordering: the DB row must be gone and the OK response sent by the time the
// handler returns. Resource teardown (worktree removal can take ~10s on large
// repos) happens in a background goroutine and must never gate the response —
// the TUI keeps the task on the list until the reply arrives.
func TestHandleDeleteTask_DeletesRowAndRespondsOK(t *testing.T) {
	s, projID := setupServerWithProject(t)
	tk, err := s.database.CreateTask(projID, "doomed", "", "doomed", "default", "main", task.StatusPending, nil)
	if err != nil {
		t.Fatal(err)
	}

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	// net.Pipe writes are synchronous, so the response must be drained
	// concurrently with the handler call.
	respCh := make(chan Message, 1)
	go func() {
		scanner := bufio.NewScanner(client)
		if scanner.Scan() {
			var m Message
			if err := json.Unmarshal(scanner.Bytes(), &m); err == nil {
				respCh <- m
			}
		}
	}()

	s.handleDeleteTask(server, DeleteTaskRequest{TaskID: tk.ID})

	// The row must already be deleted when the handler returns; cleanup runs
	// afterwards in the background and must not be what removes visibility.
	if _, err := s.database.GetTask(tk.ID); err == nil {
		t.Fatal("task row still present after handleDeleteTask returned")
	}

	select {
	case m := <-respCh:
		if m.Type != MsgOK {
			t.Fatalf("expected %s response, got %s", MsgOK, m.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no response received from handleDeleteTask")
	}
}

// TestCreateTaskFromRequest_WorkflowPinsFallback verifies that workflow pin
// fields (description, branch, target, worktree) are applied as fallbacks when
// the CreateTaskRequest leaves the corresponding field empty, and that an
// explicit request value overrides the pin.
func TestCreateTaskFromRequest_WorkflowPinsFallback(t *testing.T) {
	worktreeTrue := true

	t.Run("description branch and target pins applied when request leaves them empty", func(t *testing.T) {
		wf := config.WorkflowConfig{
			Name:     "pinned",
			Input:    "Do the thing",
			Branch:   "feat/pinned-{{task.id}}",
			Target:   "main",
			Worktree: &worktreeTrue,
			Steps:    []config.StepConfig{{Name: "implement"}},
		}
		s, _ := setupServerWithPinnedWorkflow(t, "/tmp/sortie-pin-test", wf)

		tk, _, err := s.createTaskFromRequest(CreateTaskRequest{
			ProjectPath: "/tmp/sortie-pin-test",
			Workflow:    "pinned",
			// No Input, BranchName, TargetBranch, or Worktree.
		})
		if err != nil {
			t.Fatalf("createTaskFromRequest: %v", err)
		}
		if tk.Input != "Do the thing" {
			t.Errorf("description: got %q, want %q (from workflow pin)", tk.Input, "Do the thing")
		}
		if tk.TargetBranch != "main" {
			t.Errorf("target_branch: got %q, want %q (from workflow pin)", tk.TargetBranch, "main")
		}
		if !tk.Worktree {
			t.Errorf("worktree: got false, want true (from workflow pin)")
		}
		// BranchName is the template stored before slug resolution; non-empty confirms the pin was applied.
		if tk.BranchName == "" {
			t.Errorf("branch_name: expected non-empty (from workflow branch pin)")
		}
	})

	t.Run("explicit request target overrides workflow pin", func(t *testing.T) {
		wf := config.WorkflowConfig{
			Name:     "pinned",
			Input:    "Do the thing",
			Branch:   "feat/pinned-{{task.id}}",
			Target:   "main",
			Worktree: &worktreeTrue,
			Steps:    []config.StepConfig{{Name: "implement"}},
		}
		s, _ := setupServerWithPinnedWorkflow(t, "/tmp/sortie-pin-test-2", wf)

		tk, _, err := s.createTaskFromRequest(CreateTaskRequest{
			ProjectPath:  "/tmp/sortie-pin-test-2",
			Workflow:     "pinned",
			TargetBranch: "develop", // explicit override of the "main" pin
		})
		if err != nil {
			t.Fatalf("createTaskFromRequest: %v", err)
		}
		if tk.TargetBranch != "develop" {
			t.Errorf("target_branch: got %q, want %q (explicit request should win over pin)", tk.TargetBranch, "develop")
		}
	})

	t.Run("explicit request description overrides workflow pin", func(t *testing.T) {
		wf := config.WorkflowConfig{
			Name:     "pinned",
			Input:    "Pinned description",
			Branch:   "feat/x",
			Target:   "main",
			Worktree: &worktreeTrue,
			Steps:    []config.StepConfig{{Name: "implement"}},
		}
		s, _ := setupServerWithPinnedWorkflow(t, "/tmp/sortie-pin-test-3", wf)

		tk, _, err := s.createTaskFromRequest(CreateTaskRequest{
			ProjectPath: "/tmp/sortie-pin-test-3",
			Workflow:    "pinned",
			Input:       "Override description",
		})
		if err != nil {
			t.Fatalf("createTaskFromRequest: %v", err)
		}
		if tk.Input != "Override description" {
			t.Errorf("description: got %q, want %q (explicit request should win over pin)", tk.Input, "Override description")
		}
	})

	t.Run("explicit worktree=false overrides workflow pin worktree=true", func(t *testing.T) {
		wf := config.WorkflowConfig{
			Name:     "pinned",
			Input:    "Do the thing",
			Branch:   "feat/x",
			Target:   "main",
			Worktree: &worktreeTrue,
			Steps:    []config.StepConfig{{Name: "implement"}},
		}
		s, _ := setupServerWithPinnedWorkflow(t, "/tmp/sortie-pin-test-4", wf)

		worktreeFalse := false
		tk, _, err := s.createTaskFromRequest(CreateTaskRequest{
			ProjectPath: "/tmp/sortie-pin-test-4",
			Workflow:    "pinned",
			Worktree:    &worktreeFalse, // explicit false overrides pin true
		})
		if err != nil {
			t.Fatalf("createTaskFromRequest: %v", err)
		}
		if tk.Worktree {
			t.Errorf("worktree: got true, want false (explicit request false should win over pin true)")
		}
	})

	t.Run("pin worktree=false overrides project default worktree=true", func(t *testing.T) {
		// The symmetric *bool case to the explicit-false test above: a workflow
		// that pins worktree:false must win over a project DefaultWorktree of true
		// when the request leaves worktree unset.
		worktreeFalse := false
		wf := config.WorkflowConfig{
			Name:     "no-worktree",
			Input:    "Run in place",
			Worktree: &worktreeFalse,
			Steps:    []config.StepConfig{{Name: "implement"}},
		}
		s, projID := setupServerWithPinnedWorkflow(t, "/tmp/sortie-pin-test-5", wf)
		if err := s.database.UpdateProjectDefaultWorktree(projID, true); err != nil {
			t.Fatalf("seed project default worktree: %v", err)
		}

		tk, _, err := s.createTaskFromRequest(CreateTaskRequest{
			ProjectPath: "/tmp/sortie-pin-test-5",
			Workflow:    "no-worktree",
			// No Worktree on the request — the pin must win over the project default.
		})
		if err != nil {
			t.Fatalf("createTaskFromRequest: %v", err)
		}
		if tk.Worktree {
			t.Errorf("worktree: got true, want false (pin false should win over project default true)")
		}
	})

	t.Run("checkout pin applied when request leaves branch-mode empty", func(t *testing.T) {
		// A pinned checkout drives a different path than a branch pin: it permits
		// an empty description and produces the branch-derived "⎇ <branch>" title.
		wf := config.WorkflowConfig{
			Name:     "review",
			Checkout: "release/x",
			Target:   "main",
			Worktree: &worktreeTrue,
			Steps:    []config.StepConfig{{Name: "review"}},
		}
		s, _ := setupServerWithPinnedWorkflow(t, "/tmp/sortie-pin-test-6", wf)

		tk, title, err := s.createTaskFromRequest(CreateTaskRequest{
			ProjectPath: "/tmp/sortie-pin-test-6",
			Workflow:    "review",
			// No Input, BranchName, or CheckoutBranch — checkout pin satisfies
			// the empty-description gate and supplies the checkout branch.
		})
		if err != nil {
			t.Fatalf("createTaskFromRequest: %v", err)
		}
		if tk.CheckoutBranch != "release/x" {
			t.Errorf("checkout_branch: got %q, want %q (from workflow pin)", tk.CheckoutBranch, "release/x")
		}
		if tk.Input != "" {
			t.Errorf("description: got %q, want empty (checkout pin allows empty description)", tk.Input)
		}
		if tk.BranchName != "" {
			t.Errorf("branch_name: got %q, want empty (checkout pin must not also set a new-branch template)", tk.BranchName)
		}
		if title != "⎇ release/x" {
			t.Errorf("title: got %q, want %q (branch-derived title for checkout-only task)", title, "⎇ release/x")
		}
	})

	t.Run("pin-derived worktree does not leak into persisted project default", func(t *testing.T) {
		// Core invariant: a workflow pin can set this task's worktree, but it must
		// never overwrite the project's saved DefaultWorktree — only an explicit
		// request value (a real user choice) may persist.
		wf := config.WorkflowConfig{
			Name:     "pinned",
			Input:    "Do the thing",
			Branch:   "feat/x",
			Target:   "main",
			Worktree: &worktreeTrue, // pin worktree=true
			Steps:    []config.StepConfig{{Name: "implement"}},
		}
		s, projID := setupServerWithPinnedWorkflow(t, "/tmp/sortie-pin-test-7", wf)
		// Seed the saved project default to the OPPOSITE of the pin.
		if err := s.database.UpdateProjectDefaultWorktree(projID, false); err != nil {
			t.Fatalf("seed project default worktree: %v", err)
		}

		tk, _, err := s.createTaskFromRequest(CreateTaskRequest{
			ProjectPath: "/tmp/sortie-pin-test-7",
			Workflow:    "pinned",
			// No Worktree on the request — only the pin sets it.
		})
		if err != nil {
			t.Fatalf("createTaskFromRequest: %v", err)
		}
		if !tk.Worktree {
			t.Errorf("task worktree: got false, want true (pin should apply to the task)")
		}
		proj, err := s.database.GetProject(projID)
		if err != nil {
			t.Fatalf("GetProject: %v", err)
		}
		if proj.DefaultWorktree {
			t.Errorf("persisted project DefaultWorktree: got true, want false (pin-derived worktree must not leak into project defaults)")
		}
	})
}
