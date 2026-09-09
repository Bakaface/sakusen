package daemon

import (
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/Bakaface/sakusen/internal/workflow"
)

// parallelWorkflow is the shape every test in this file primes: a plain step,
// a two-branch group, and a synthesis step.
func parallelWorkflow() *config.WorkflowConfig {
	return &config.WorkflowConfig{
		Name: "reviewed",
		Steps: []config.StepConfig{
			{Name: "implement", Prompt: "i"},
			{
				Name: "review",
				Parallel: &config.ParallelConfig{
					Require: "any",
					Branches: []config.StepConfig{
						{Name: "review-a", Prompt: "a", Agent: "alt"},
						{Name: "review-b", Prompt: "b"},
					},
				},
			},
			{Name: "synthesize", Prompt: "{{steps.review.context}}"},
		},
	}
}

// primeParallelProject caches a project context whose workflow contains a
// parallel group, mirroring getProjectContext's construction.
func primeParallelProject(t *testing.T, s *Server, projID int64) *config.Config {
	t.Helper()
	wf := parallelWorkflow()
	cfg := &config.Config{
		Workflows: []config.WorkflowConfig{*wf},
		Agents: map[string]config.AgentConfig{
			"claude": {Command: "claude -p"},
			"alt":    {Command: "alt"},
		},
	}
	repoRoot := "/tmp/sakusen-test"
	s.projectsMu.Lock()
	s.projects[projID] = &projectContext{
		cfg:               cfg,
		engine:            workflow.NewEngine(cfg, s.database, s.notifier, repoRoot),
		repoRoot:          repoRoot,
		tracksFingerprint: tracksFingerprint(repoRoot),
	}
	s.projectsMu.Unlock()
	return cfg
}

func newParallelTask(t *testing.T, s *Server, projID int64) *task.Task {
	t.Helper()
	tk, err := s.database.CreateTask(projID, "reviewed task", "desc", "slug", "reviewed", "main", task.StatusRunning, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestHandleGetTaskStepsEmitsBranchRows(t *testing.T) {
	s, projID := setupServerWithProject(t)
	primeParallelProject(t, s, projID)
	tk := newParallelTask(t, s, projID)

	// implement completed, review-a completed, review-b failed, group running.
	ctxA := "A findings"
	for _, name := range []string{"implement", "review", "review-a", "review-b"} {
		if err := s.database.CreateTaskStep(tk.ID, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.database.CompleteTaskStep(tk.ID, "implement", nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.database.CompleteTaskStep(tk.ID, "review-a", &ctxA, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.database.FailTaskStep(tk.ID, "review-b", 1); err != nil {
		t.Fatal(err)
	}

	clientConn, serverConn := pipeForHandler(t)
	go s.handleGetTaskSteps(serverConn, GetTaskStepsRequest{TaskID: tk.ID})

	msg := readOneMessage(t, clientConn)
	if msg.Type != MsgGetTaskSteps {
		t.Fatalf("expected MsgGetTaskSteps, got %s: %s", msg.Type, string(msg.Payload))
	}
	var resp GetTaskStepsResponse
	if err := msg.DecodePayload(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	wantOrder := []string{"implement", "review", "review-a", "review-b", "synthesize"}
	var gotOrder []string
	for _, d := range resp.Steps {
		gotOrder = append(gotOrder, d.Name)
	}
	if strings.Join(gotOrder, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("step order = %v, want %v (group followed by its branches)", gotOrder, wantOrder)
	}

	byName := map[string]TaskStepDetail{}
	for _, d := range resp.Steps {
		byName[d.Name] = d
	}
	if byName["review-a"].Parent != "review" || byName["review-b"].Parent != "review" {
		t.Errorf("branch rows must carry Parent=review, got %q/%q", byName["review-a"].Parent, byName["review-b"].Parent)
	}
	if byName["review"].Parent != "" {
		t.Errorf("group row Parent = %q, want empty", byName["review"].Parent)
	}
	if byName["review-b"].Status != "failed" {
		t.Errorf("review-b status = %q, want failed", byName["review-b"].Status)
	}
	if byName["synthesize"].Status != "pending" {
		t.Errorf("synthesize status = %q, want pending", byName["synthesize"].Status)
	}
	if byName["review-a"].Agent != "alt" {
		t.Errorf("review-a agent = %q, want the branch's explicit slug", byName["review-a"].Agent)
	}
	if byName["review-b"].Agent != "claude" {
		t.Errorf("review-b agent = %q, want the cascade default", byName["review-b"].Agent)
	}
	if byName["review"].Agent != "" {
		t.Errorf("group agent = %q, want empty (a group runs no agent)", byName["review"].Agent)
	}
}

func TestTaskToInfoBranchStatus(t *testing.T) {
	s, projID := setupServerWithProject(t)
	primeParallelProject(t, s, projID)
	tk := newParallelTask(t, s, projID)

	if err := s.database.CreateTaskStep(tk.ID, "review-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.database.CompleteTaskStep(tk.ID, "review-a", nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.database.CreateTaskStep(tk.ID, "review-b"); err != nil {
		t.Fatal(err)
	}

	info := s.taskToInfo(tk)
	if got := info.BranchStatus["review-a"]; got != "completed" {
		t.Errorf("BranchStatus[review-a] = %q, want completed", got)
	}
	if got := info.BranchStatus["review-b"]; got != "running" {
		t.Errorf("BranchStatus[review-b] = %q, want running", got)
	}
	if _, ok := info.BranchStatus["review"]; ok {
		t.Error("the group itself must not appear in BranchStatus")
	}
}

func TestTaskToInfoBranchStatusAbsentWithoutGroup(t *testing.T) {
	s, projID := setupServerWithProject(t)
	cfg := &config.Config{Workflows: []config.WorkflowConfig{{
		Name:  "plain",
		Steps: []config.StepConfig{{Name: "implement", Prompt: "i"}},
	}}}
	repoRoot := "/tmp/sakusen-test"
	s.projectsMu.Lock()
	s.projects[projID] = &projectContext{
		cfg:               cfg,
		engine:            workflow.NewEngine(cfg, s.database, s.notifier, repoRoot),
		repoRoot:          repoRoot,
		tracksFingerprint: tracksFingerprint(repoRoot),
	}
	s.projectsMu.Unlock()

	tk, err := s.database.CreateTask(projID, "plain task", "d", "s", "plain", "main", task.StatusRunning, nil)
	if err != nil {
		t.Fatal(err)
	}
	if info := s.taskToInfo(tk); info.BranchStatus != nil {
		t.Errorf("BranchStatus = %v, want nil for a workflow with no parallel group", info.BranchStatus)
	}
}

func TestRetryParallelGroupKeepsCompletedBranches(t *testing.T) {
	s, projID := setupServerWithProject(t)
	primeParallelProject(t, s, projID)
	tk := newParallelTask(t, s, projID)

	ctxA := "A findings"
	implCtx := "implemented"
	for _, name := range []string{"implement", "review", "review-a", "review-b", "synthesize"} {
		if err := s.database.CreateTaskStep(tk.ID, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.database.CompleteTaskStep(tk.ID, "implement", &implCtx, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.database.CompleteTaskStep(tk.ID, "review-a", &ctxA, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.database.FailTaskStep(tk.ID, "review-b", 1); err != nil {
		t.Fatal(err)
	}

	clientConn, serverConn := pipeForHandler(t)
	go s.handleRetryTask(serverConn, RetryTaskRequest{TaskID: tk.ID, StepName: "review"})
	if msg := readOneMessage(t, clientConn); msg.Type != MsgRetryTask {
		t.Fatalf("expected MsgRetryTask, got %s: %s", msg.Type, string(msg.Payload))
	}

	rows, err := s.database.GetTaskStepRows(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rows["review-a"]; !ok {
		t.Error("a completed branch row must survive a group retry so it is not re-run")
	}
	for _, gone := range []string{"review", "review-b", "synthesize"} {
		if _, ok := rows[gone]; ok {
			t.Errorf("row %q should have been deleted by the group retry", gone)
		}
	}
	if _, ok := rows["implement"]; !ok {
		t.Error("an earlier step's row must be preserved")
	}
}

func TestRetryBranchNameRejected(t *testing.T) {
	s, projID := setupServerWithProject(t)
	primeParallelProject(t, s, projID)
	tk := newParallelTask(t, s, projID)

	clientConn, serverConn := pipeForHandler(t)
	go s.handleRetryTask(serverConn, RetryTaskRequest{TaskID: tk.ID, StepName: "review-b"})

	msg := readOneMessage(t, clientConn)
	if msg.Type != MsgError {
		t.Fatalf("expected MsgError, got %s", msg.Type)
	}
	var resp ErrorResponse
	if err := msg.DecodePayload(&resp); err != nil {
		t.Fatal(err)
	}
	want := `step "review-b" is a branch of parallel group "review"; retry the group`
	if !strings.Contains(resp.Message, want) {
		t.Errorf("error = %q, want it to contain %q", resp.Message, want)
	}
}

func TestUpdateActiveStepContextFromBranch(t *testing.T) {
	s, projID := setupServerWithProject(t)
	primeParallelProject(t, s, projID)
	tk := newParallelTask(t, s, projID)

	// The group is the cursor slot; the branch has its own running row.
	if err := s.database.UpdateTaskStep(tk.ID, 1, "review"); err != nil {
		t.Fatal(err)
	}
	if err := s.database.CreateTaskStep(tk.ID, "review"); err != nil {
		t.Fatal(err)
	}
	if err := s.database.CreateTaskStep(tk.ID, "review-a"); err != nil {
		t.Fatal(err)
	}

	clientConn, serverConn := pipeForHandler(t)
	go s.handleUpdateActiveStepContext(serverConn, UpdateActiveStepContextRequest{
		TaskID:   tk.ID,
		StepName: "review-a",
		Context:  "branch artifact",
	})
	if msg := readOneMessage(t, clientConn); msg.Type != MsgOK {
		t.Fatalf("expected MsgOK for a branch of the running group, got %s: %s", msg.Type, string(msg.Payload))
	}

	rows, err := s.database.GetTaskStepRows(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rows["review-a"].Context != "branch artifact" {
		t.Errorf("branch context = %q, want the manual write", rows["review-a"].Context)
	}
}

func TestUpdateActiveStepContextRejectsGroupName(t *testing.T) {
	s, projID := setupServerWithProject(t)
	primeParallelProject(t, s, projID)
	tk := newParallelTask(t, s, projID)

	if err := s.database.UpdateTaskStep(tk.ID, 1, "review"); err != nil {
		t.Fatal(err)
	}
	if err := s.database.CreateTaskStep(tk.ID, "review"); err != nil {
		t.Fatal(err)
	}

	clientConn, serverConn := pipeForHandler(t)
	go s.handleUpdateActiveStepContext(serverConn, UpdateActiveStepContextRequest{
		TaskID:   tk.ID,
		StepName: "review",
		Context:  "should not stick",
	})

	msg := readOneMessage(t, clientConn)
	if msg.Type != MsgError {
		t.Fatalf("expected MsgError, got %s", msg.Type)
	}
	var resp ErrorResponse
	if err := msg.DecodePayload(&resp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Message, `"review" is a parallel group; branches publish their own context`) {
		t.Errorf("error = %q, want the group-write rejection", resp.Message)
	}
}

func TestUpdateActiveStepContextRejectsUnrelatedStep(t *testing.T) {
	s, projID := setupServerWithProject(t)
	primeParallelProject(t, s, projID)
	tk := newParallelTask(t, s, projID)

	if err := s.database.UpdateTaskStep(tk.ID, 1, "review"); err != nil {
		t.Fatal(err)
	}
	if err := s.database.CreateTaskStep(tk.ID, "review"); err != nil {
		t.Fatal(err)
	}

	clientConn, serverConn := pipeForHandler(t)
	go s.handleUpdateActiveStepContext(serverConn, UpdateActiveStepContextRequest{
		TaskID:   tk.ID,
		StepName: "implement",
		Context:  "should not stick",
	})

	msg := readOneMessage(t, clientConn)
	if msg.Type != MsgError {
		t.Fatalf("expected MsgError, got %s", msg.Type)
	}
	var resp ErrorResponse
	if err := msg.DecodePayload(&resp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Message, `step "implement" is not the active step`) {
		t.Errorf("error = %q, want the non-active-step rejection", resp.Message)
	}
}

func TestSummarizeWorkflowsIncludesParallel(t *testing.T) {
	wf := parallelWorkflow()
	cfg := &config.Config{
		Workflows: []config.WorkflowConfig{*wf},
		Agents: map[string]config.AgentConfig{
			"claude": {Command: "claude -p"},
			"alt":    {Command: "alt"},
		},
	}
	summaries := summarizeWorkflows(cfg, cfg.Workflows)
	if len(summaries) != 1 {
		t.Fatalf("summaries = %d, want 1", len(summaries))
	}
	steps := summaries[0].Steps
	if len(steps) != 3 {
		t.Fatalf("step summaries = %d, want 3 (a group is ONE step slot)", len(steps))
	}
	group := steps[1]
	if group.Parallel == nil {
		t.Fatal("group step summary is missing its Parallel projection")
	}
	if group.Parallel.Require != "any" {
		t.Errorf("require = %q, want any", group.Parallel.Require)
	}
	if len(group.Parallel.Branches) != 2 {
		t.Fatalf("branch summaries = %d, want 2", len(group.Parallel.Branches))
	}
	if group.Parallel.Branches[0].Name != "review-a" || group.Parallel.Branches[0].Agent != "alt" {
		t.Errorf("branch 0 = %+v, want review-a/alt", group.Parallel.Branches[0])
	}
	if group.Parallel.Branches[1].Agent != "claude" {
		t.Errorf("branch 1 agent = %q, want the cascade default", group.Parallel.Branches[1].Agent)
	}
	if group.Agent != "" {
		t.Errorf("group agent = %q, want empty", group.Agent)
	}
}

func TestRetryEarlierStepDropsLaterGroupBranches(t *testing.T) {
	// Retrying from a step BEFORE the group must re-run the whole group: its
	// branches reviewed the work the retry is about to redo, so their completed
	// rows are stale. Only the retried group itself keeps completed branches.
	s, projID := setupServerWithProject(t)
	primeParallelProject(t, s, projID)
	tk := newParallelTask(t, s, projID)

	ctx := "x"
	for _, name := range []string{"implement", "review", "review-a", "review-b", "synthesize"} {
		if err := s.database.CreateTaskStep(tk.ID, name); err != nil {
			t.Fatal(err)
		}
		if err := s.database.CompleteTaskStep(tk.ID, name, &ctx, 0); err != nil {
			t.Fatal(err)
		}
	}

	clientConn, serverConn := pipeForHandler(t)
	go s.handleRetryTask(serverConn, RetryTaskRequest{TaskID: tk.ID, StepName: "implement"})
	if msg := readOneMessage(t, clientConn); msg.Type != MsgRetryTask {
		t.Fatalf("expected MsgRetryTask, got %s: %s", msg.Type, string(msg.Payload))
	}

	rows, err := s.database.GetTaskStepRows(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("retry from the first step must clear every row, including completed branch rows of later groups; left: %v", rows)
	}
}
