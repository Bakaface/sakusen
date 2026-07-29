package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/task"
	"github.com/Bakaface/sortie/internal/tmux"
	"github.com/Bakaface/sortie/internal/workflow"
)

const (
	// titleGenerationTimeout is the maximum time allowed for AI-based task title generation.
	titleGenerationTimeout = 30 * time.Second
)

// noiseFiles are files that don't count as meaningful changes when checking
// whether a task produced real output (e.g. when fast-tracking to completed).
var noiseFiles = []string{".claude-output.log", "CLAUDE.md"}

// tmuxFirstTitle returns a placeholder title for a tmux-first workflow task that
// was created without a description. Falls back to a generic label if the
// workflow is unnamed.
func tmuxFirstTitle(wf *config.WorkflowConfig) string {
	if wf != nil && wf.Name != "" {
		return "tmux: " + wf.Name
	}
	return "tmux session"
}

func (s *Server) handleListTasks(conn net.Conn, req ListTasksRequest) {
	var tasks []*task.Task
	var err error

	if req.ProjectID > 0 {
		tasks, err = s.database.GetTasksByProject(req.ProjectID)
	} else if req.ProjectName != "" {
		tasks, err = s.database.GetTasksByProjectName(req.ProjectName)
	} else {
		tasks, err = s.database.GetAllTasks()
	}
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get tasks: %v", err))
		return
	}

	infos := make([]TaskInfo, len(tasks))
	for i, t := range tasks {
		infos[i] = s.taskToInfo(t)
	}

	s.sendMessage(conn, MsgTaskList, TaskListResponse{Tasks: infos})
}

func (s *Server) handleGetTask(conn net.Conn, req GetTaskRequest) {
	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get task: %v", err))
		return
	}

	info := s.taskToInfo(t)
	s.sendMessage(conn, MsgGetTask, GetTaskResponse{Task: info})
}

func (s *Server) handleCreateTask(conn net.Conn, req CreateTaskRequest) {
	t, _, err := s.createTaskFromRequest(req)
	if err != nil {
		s.sendError(conn, err.Error())
		return
	}

	s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: s.taskToInfo(t)})
	s.sendMessage(conn, MsgCreateTask, CreateTaskResponse{Task: s.taskToInfo(t)})

	s.kickOffPostCreate(t, req)
}

// createTaskFromRequest validates and persists a CreateTaskRequest, returning
// the created task plus the resolved title (used by callers that need it for
// the async post-create hook). User-facing errors are returned plainly so the
// caller can propagate them on whatever transport it owns.
//
// Shared by handleCreateTask and handleCreateTasksAndWait so the bundled
// spawn-and-suspend path uses exactly the same validation, dependency
// auto-collection, and project-defaults persistence as the single-create path.
func (s *Server) createTaskFromRequest(req CreateTaskRequest) (*task.Task, string, error) {
	description := strings.TrimSpace(req.Description)

	// Normalize the explicit-none track sentinel ("none" = explicitly
	// trackless) so it works uniformly from every surface — most importantly
	// create_tasks_and_wait children opting out of parent-track inheritance.
	if strings.EqualFold(strings.TrimSpace(req.Track), "none") {
		req.Track = ""
	}

	projectPath := req.ProjectPath
	if projectPath == "" {
		return nil, "", fmt.Errorf("project_path is required")
	}

	proj, err := s.database.GetOrCreateProject(projectPath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to resolve project: %v", err)
	}

	// Resolve workflow against the project config so we can decide whether
	// empty descriptions are permitted (tmux-first workflows allow them).
	projCfg := s.cfg
	if pc, err := s.getProjectContext(proj.ID); err == nil {
		projCfg = pc.cfg
	}

	// Resolve the track (slug or numeric ID; project shadows global). An
	// unknown track fails the create — silently dropping a requested track
	// association would be worse than erroring.
	var tr *task.Track
	if req.Track != "" {
		tr, err = s.resolveTrackRef(&proj.ID, req.Track)
		if err != nil {
			return nil, "", err
		}
	}

	// Workflow precedence: explicit request > track's workflow field >
	// project default. When the name came FROM the track, validate it resolves
	// (GetWorkflow silently falls back to the default workflow, so it cannot
	// validate); explicit req.Workflow keeps today's silent-fallback behavior.
	workflowName := req.Workflow
	if workflowName == "" && tr != nil && tr.Workflow != "" {
		workflowName = tr.Workflow
		if projCfg.GetTaskWorkflow(workflowName) == nil {
			return nil, "", fmt.Errorf("track %q references unknown workflow %q", tr.Slug, workflowName)
		}
	}

	wf := projCfg.GetWorkflow(workflowName)
	tmuxFirst := wf != nil && wf.FirstStepIsTmux()

	// Apply workflow-level pins as fallbacks below explicit request values.
	// Precedence: explicit request value > workflow config pin > project default.
	if description == "" && wf != nil {
		description = strings.TrimSpace(wf.Description)
	}

	// Branch/checkout pins form a mutually-exclusive pair (branch-mode choice).
	// Apply them only when the request specified neither, so an explicit
	// --branch/--checkout fully overrides the workflow's branch-mode pin.
	branchName := req.BranchName
	checkoutBranch := req.CheckoutBranch
	if branchName == "" && checkoutBranch == "" && wf != nil {
		branchName = wf.Branch
		checkoutBranch = wf.Checkout
	}

	targetBranch := req.TargetBranch
	if targetBranch == "" && wf != nil {
		targetBranch = wf.Target
	}

	if description == "" && checkoutBranch == "" && !tmuxFirst {
		return nil, "", fmt.Errorf("description cannot be empty")
	}

	// Validate any {{tasks.<id>.<field>}} references and collect auto-blockers.
	// selfID is 0 — the task row doesn't exist yet, so no self-references are
	// possible (any future cycle would have to be in req.BlockedBy explicitly).
	autoBlockedBy, refErr := s.validateTaskRefs(description, proj.ID, 0, "description")
	if refErr != nil {
		return nil, "", refErr
	}

	// Caller-supplied title wins. Otherwise: branch-derived for checkout-only,
	// workflow-derived for tmux-first with no description, else sanitized description.
	var title string
	switch {
	case strings.TrimSpace(req.Title) != "":
		title = strings.TrimSpace(req.Title)
	case description == "" && checkoutBranch != "":
		title = "⎇ " + checkoutBranch
	case description == "" && tmuxFirst:
		title = tmuxFirstTitle(wf)
	default:
		title = task.SanitizeTitle(description)
	}

	slug := task.Slugify(title)

	priority := task.PriorityMedium
	if req.Priority != "" && task.IsValidPriority(req.Priority) {
		priority = task.Priority(req.Priority)
	} else if proj.DefaultPriority != "" {
		priority = proj.DefaultPriority
	}

	if checkoutBranch != "" && branchName != "" {
		return nil, "", fmt.Errorf("cannot specify both --checkout and --branch")
	}

	// Worktree precedence: explicit request > workflow pin > project default.
	worktree := proj.DefaultWorktree
	if wf != nil && wf.Worktree != nil {
		worktree = *wf.Worktree
	}
	if req.Worktree != nil {
		worktree = *req.Worktree
	}

	// Persist form preferences for this project. Only user-driven choices
	// (explicit request values) should overwrite the saved project defaults —
	// pin-derived worktree values must not leak into the persisted default.
	persistWorktree := proj.DefaultWorktree
	if req.Worktree != nil {
		persistWorktree = *req.Worktree
	}
	branchMode := 0
	if req.BranchMode != nil {
		branchMode = *req.BranchMode
	}
	if err := s.database.UpdateProjectDefaults(proj.ID, persistWorktree, branchMode, req.Workflow); err != nil {
		log.Printf("%sFailed to update project defaults for project %d: %v", s.projectLogPrefix(proj.ID), proj.ID, err)
	}

	var trackID *int64
	if tr != nil {
		trackID = &tr.ID
	}
	t, err := s.database.CreateTaskWithPriority(proj.ID, title, description, slug, workflowName, branchName, "", targetBranch, checkoutBranch, task.StatusInit, priority, worktree, req.Images, trackID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create task: %v", err)
	}

	// Merge auto-blockers (from {{tasks.<id>.<field>}} refs) into req.BlockedBy,
	// deduplicating so an explicit --blocked-by overlap doesn't double-insert.
	blockedBy := mergeBlockedBy(req.BlockedBy, autoBlockedBy)

	if len(blockedBy) > 0 {
		if err := s.database.SetTaskDependencies(t.ID, blockedBy); err != nil {
			log.Printf("%sFailed to set dependencies for task #%d: %v", s.projectLogPrefix(proj.ID), t.ID, err)
		} else {
			if updated, err := s.database.GetTask(t.ID); err == nil {
				t = updated
			}
		}
	}

	return t, title, nil
}

// kickOffPostCreate starts the async title/branch refinement (or tmux setup)
// that transitions a newly-created task out of StatusInit. Extracted so
// handleCreateTask and handleCreateTasksAndWait fire the same goroutine on
// every child without duplicating the branch logic.
func (s *Server) kickOffPostCreate(t *task.Task, req CreateTaskRequest) {
	title := t.Title
	// Use the persisted (resolved) description, not req.Description: a workflow
	// may have pinned the description when the request left it empty, and title
	// refinement must see the resolved value (otherwise AI title generation is
	// wrongly skipped for pinned-description tasks).
	description := strings.TrimSpace(t.Description)
	if req.TmuxDirect {
		go s.setupTmuxDirect(t.ID, t.ProjectID, title)
	} else {
		go s.refineTaskTitle(t.ID, t.ProjectID, t.BranchName, t.Worktree, t.CheckoutBranch, description, title, req.Title)
	}
}

func (s *Server) handleDeleteTask(conn net.Conn, req DeleteTaskRequest) {
	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get task: %v", err))
		return
	}

	agentID := fmt.Sprintf("%d", t.ID)
	_ = s.manager.StopAgent(agentID)

	// Delete the DB row first and respond immediately: this is the
	// authoritative state change and takes milliseconds. The heavyweight
	// resource teardown (removing a large worktree can take ~10s) runs in
	// the background — it is best-effort and only needs the task snapshot
	// already loaded above, not the row.
	if err := s.database.DeleteTask(t.ID); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to delete task: %v", err))
		return
	}

	s.sendMessage(conn, MsgOK, OKResponse{Message: fmt.Sprintf("task #%d deleted", t.ID)})

	go s.cleanupDeletedTaskResources(t)
}

// cleanupDeletedTaskResources tears down the runtime resources of an
// already-deleted task: tmux sessions, git worktree + branch, and the log
// directory. It runs after the task row is gone so slow filesystem work never
// delays the delete response. Every step is best-effort (logged, not
// returned); the ClearWorktreePath call inside cleanupWorktreeAndBranch is a
// harmless no-op UPDATE once the row has been removed.
func (s *Server) cleanupDeletedTaskResources(t *task.Task) {
	agentID := fmt.Sprintf("%d", t.ID)

	if pc, err := s.getProjectContext(t.ProjectID); err == nil {
		if err := tmux.KillSessionsForTask(pc.cfg.Project.Name, agentID); err != nil {
			log.Printf("%sWarning: failed to kill tmux sessions for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
		}
	}

	repoRoot := s.getProjectRepoRoot(t)

	if t.Worktree && repoRoot != "" {
		if pc, err := s.getProjectContext(t.ProjectID); err == nil {
			s.cleanupWorktreeAndBranch(pc, t)
		}
	}

	dataDir := s.getProjectDataDir(t)
	logDir := workflow.ProjectLogsDir(dataDir, t.ID)
	if err := os.RemoveAll(logDir); err != nil {
		log.Printf("%sWarning: failed to remove log dir for task #%d: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
	}
}

func (s *Server) handleRetryTask(conn net.Conn, req RetryTaskRequest) {
	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get task: %v", err))
		return
	}

	// Kill any stale tmux sessions for this task
	agentID := fmt.Sprintf("%d", req.TaskID)
	if pc, err := s.getProjectContext(t.ProjectID); err == nil {
		if err := tmux.KillSessionsForTask(pc.cfg.Project.Name, agentID); err != nil {
			log.Printf("%sWarning: failed to kill tmux sessions for task #%d: %v", s.projectLogPrefix(t.ProjectID), req.TaskID, err)
		}
	}

	// Stop any running agent for this task
	_ = s.manager.StopAgent(agentID)

	// Full retry (legacy path) when no specific step is requested or the
	// chosen step is the first step in the workflow — both are semantically
	// equivalent and the legacy reset is simpler.
	if req.StepName == "" {
		if err := s.database.ResetTaskForRetry(req.TaskID); err != nil {
			s.sendError(conn, fmt.Sprintf("failed to reset task: %v", err))
			return
		}
		refreshed, err := s.database.GetTask(req.TaskID)
		if err != nil {
			s.sendError(conn, fmt.Sprintf("failed to load task after retry: %v", err))
			return
		}
		s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: s.taskToInfo(refreshed)})
		s.sendMessage(conn, MsgRetryTask, RetryTaskResponse{Task: s.taskToInfo(refreshed)})
		return
	}

	// Per-step retry: look up the workflow, find the chosen step, and reset
	// state only from that step onward. Earlier completed work is preserved.
	pc, err := s.getProjectContext(t.ProjectID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get project context: %v", err))
		return
	}
	wf := pc.cfg.GetWorkflow(t.Workflow)
	if wf == nil {
		s.sendError(conn, fmt.Sprintf("workflow %q not found", t.Workflow))
		return
	}
	stepIdx := -1
	for i, st := range wf.Steps {
		if st.Name == req.StepName {
			stepIdx = i
			break
		}
	}
	if stepIdx < 0 {
		s.sendError(conn, fmt.Sprintf("step %q not found in workflow %q", req.StepName, t.Workflow))
		return
	}

	// First step → full retry (avoids the from-step delete dance when there's
	// nothing prior to preserve).
	if stepIdx == 0 {
		if err := s.database.ResetTaskForRetry(req.TaskID); err != nil {
			s.sendError(conn, fmt.Sprintf("failed to reset task: %v", err))
			return
		}
		refreshed, err := s.database.GetTask(req.TaskID)
		if err != nil {
			s.sendError(conn, fmt.Sprintf("failed to load task after retry: %v", err))
			return
		}
		s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: s.taskToInfo(refreshed)})
		s.sendMessage(conn, MsgRetryTask, RetryTaskResponse{Task: s.taskToInfo(refreshed)})
		return
	}

	stepsFromIdx := make([]string, 0, len(wf.Steps)-stepIdx)
	for _, st := range wf.Steps[stepIdx:] {
		stepsFromIdx = append(stepsFromIdx, st.Name)
	}
	if err := s.database.ResetTaskForRetryAtStep(req.TaskID, stepIdx, stepsFromIdx); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to reset task: %v", err))
		return
	}

	refreshed, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to load task after retry: %v", err))
		return
	}
	s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: s.taskToInfo(refreshed)})
	s.sendMessage(conn, MsgRetryTask, RetryTaskResponse{Task: s.taskToInfo(refreshed)})
}

func (s *Server) handleUpdatePriority(conn net.Conn, req UpdatePriorityRequest) {
	if !task.IsValidPriority(req.Priority) {
		s.sendError(conn, fmt.Sprintf("invalid priority: %s", req.Priority))
		return
	}

	if err := s.database.UpdateTaskPriority(req.TaskID, task.Priority(req.Priority)); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to update priority: %v", err))
		return
	}

	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to load task after priority update: %v", err))
		return
	}
	s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: s.taskToInfo(t)})
	s.sendMessage(conn, MsgUpdatePriority, UpdatePriorityResponse{Task: s.taskToInfo(t)})
}

func (s *Server) handleUpdateField(conn net.Conn, req UpdateFieldRequest) {
	// For description/context edits, validate any {{tasks.<id>.<field>}} refs
	// against the new value and collect newly active references as auto-blockers.
	// Validation runs before the mutation so a bad ref leaves the field untouched.
	var autoBlockedBy []int64
	if req.Field == "description" || req.Field == "context" {
		t, err := s.database.GetTask(req.TaskID)
		if err != nil {
			s.sendError(conn, fmt.Sprintf("failed to get task: %v", err))
			return
		}
		auto, refErr := s.validateTaskRefs(req.Value, t.ProjectID, req.TaskID, req.Field)
		if refErr != nil {
			s.sendError(conn, refErr.Error())
			return
		}
		autoBlockedBy = auto
	}

	var err error
	switch req.Field {
	case "title":
		err = s.database.UpdateTaskTitle(req.TaskID, req.Value)
	case "description":
		err = s.database.UpdateTaskDescription(req.TaskID, req.Value)
	case "context":
		err = s.database.UpdateTaskContext(req.TaskID, req.Value)
	default:
		s.sendError(conn, fmt.Sprintf("unknown field: %s", req.Field))
		return
	}
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to update %s: %v", req.Field, err))
		return
	}

	// Additive only — never remove existing edges (user may have added some
	// manually). AddTaskDependency is an INSERT OR IGNORE so duplicates are
	// harmless.
	for _, dep := range autoBlockedBy {
		if err := s.database.AddTaskDependency(req.TaskID, dep); err != nil {
			log.Printf("Warning: failed to auto-add dependency %d -> %d: %v", req.TaskID, dep, err)
		}
	}

	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to load task after field update: %v", err))
		return
	}
	s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: s.taskToInfo(t)})
	s.sendMessage(conn, MsgUpdateField, UpdateFieldResponse{Task: s.taskToInfo(t)})
}

func (s *Server) handleRevertTask(conn net.Conn, req RevertTaskRequest) {
	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get task: %v", err))
		return
	}

	if !t.Status.IsTerminal() {
		s.sendError(conn, fmt.Sprintf("task must be completed or failed to revert (status: %s)", t.Status))
		return
	}

	commits, err := s.database.GetTaskCommits(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get task commits: %v", err))
		return
	}

	if len(commits) == 0 {
		s.sendError(conn, "no commits found for this task")
		return
	}

	pc, err := s.getProjectContext(t.ProjectID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get project context: %v", err))
		return
	}

	// Serialize against in-progress merges via the per-repo merge coordinator —
	// revert mutates the base repo so it must not race with MergeBranch.
	var revertErr error
	pc.engine.Coord().Lock().WithLock(func() {
		revertErr = pc.repo.RevertCommits(commits)
	})
	if revertErr != nil {
		s.sendError(conn, fmt.Sprintf("failed to revert commits: %v", revertErr))
		return
	}

	log.Printf("%sTask #%d reverted (%d commits)", s.projectLogPrefix(t.ProjectID), t.ID, len(commits))

	refreshed, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to load task after revert: %v", err))
		return
	}
	s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: s.taskToInfo(refreshed)})
	s.sendMessage(conn, MsgRevertTask, RevertTaskResponse{Task: s.taskToInfo(refreshed)})
}

func (s *Server) handleUpdateDependency(conn net.Conn, req UpdateDependencyRequest) {
	// Validate both tasks exist
	if _, err := s.database.GetTask(req.TaskID); err != nil {
		s.sendError(conn, fmt.Sprintf("task #%d not found: %v", req.TaskID, err))
		return
	}
	if _, err := s.database.GetTask(req.BlockedBy); err != nil {
		s.sendError(conn, fmt.Sprintf("task #%d not found: %v", req.BlockedBy, err))
		return
	}

	switch req.Action {
	case "add":
		// Check for circular dependency
		circular, err := s.database.HasCircularDependency(req.TaskID, req.BlockedBy)
		if err != nil {
			s.sendError(conn, fmt.Sprintf("failed to check circular dependency: %v", err))
			return
		}
		if circular {
			s.sendError(conn, "adding this dependency would create a cycle")
			return
		}
		if err := s.database.AddTaskDependency(req.TaskID, req.BlockedBy); err != nil {
			s.sendError(conn, fmt.Sprintf("failed to add dependency: %v", err))
			return
		}
	case "remove":
		if err := s.database.RemoveTaskDependency(req.TaskID, req.BlockedBy); err != nil {
			s.sendError(conn, fmt.Sprintf("failed to remove dependency: %v", err))
			return
		}
	default:
		s.sendError(conn, fmt.Sprintf("invalid action: %s (must be 'add' or 'remove')", req.Action))
		return
	}

	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to load task after dependency update: %v", err))
		return
	}
	s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: s.taskToInfo(t)})
	s.sendMessage(conn, MsgUpdateDependency, UpdateDependencyResponse{Task: s.taskToInfo(t)})
}

func (s *Server) handleGetStepContexts(conn net.Conn, req GetStepContextsRequest) {
	steps, err := s.database.GetAllTaskStepContexts(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get step contexts: %v", err))
		return
	}
	s.sendMessage(conn, MsgGetStepContexts, GetStepContextsResponse{Steps: steps})
}

func (s *Server) handleGetTaskSteps(conn net.Conn, req GetTaskStepsRequest) {
	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get task: %v", err))
		return
	}

	projCfg := s.cfg
	if pc, err := s.getProjectContext(t.ProjectID); err == nil {
		projCfg = pc.cfg
	}

	wf := projCfg.GetWorkflow(t.Workflow)
	if wf == nil {
		s.sendMessage(conn, MsgGetTaskSteps, GetTaskStepsResponse{Steps: nil})
		return
	}

	rows, err := s.database.GetTaskStepRows(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get step rows: %v", err))
		return
	}

	details := make([]TaskStepDetail, 0, len(wf.Steps))
	for _, step := range wf.Steps {
		d := TaskStepDetail{Name: step.Name, Status: "pending"}
		if row, ok := rows[step.Name]; ok {
			d.Status = row.Status
			d.Context = row.Context
			if row.CompletedAt.Valid {
				ts := row.CompletedAt.Time
				d.CompletedAt = &ts
			}
		}
		details = append(details, d)
	}

	s.sendMessage(conn, MsgGetTaskSteps, GetTaskStepsResponse{Steps: details})
}

func (s *Server) handleUpdateStepContext(conn net.Conn, req UpdateStepContextRequest) {
	if strings.TrimSpace(req.StepName) == "" {
		s.sendError(conn, "step_name is required")
		return
	}
	if err := s.database.UpdateTaskStepContext(req.TaskID, req.StepName, req.Context); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to update step context: %v", err))
		return
	}
	s.broadcastTaskUpdate(req.TaskID)
	s.sendMessage(conn, MsgOK, OKResponse{Message: fmt.Sprintf("step %q context updated", req.StepName)})
}

// handleUpdateActiveStepContext writes context for the task's currently-active
// (running) step. Used by the MCP update_step_context tool so an agent can
// publish a canonical artifact mid-session instead of waiting for the post-
// session summarizer. The handler enforces the safety invariant that the step
// must be the task's current step; which task_steps row that write actually
// targets (running vs. paused-tmux) is decided entirely inside the Engine —
// see workflow.Engine.ResolveActiveStep / PublishManualStepContext in
// internal/workflow/stepcontext.go. The daemon carries the resulting
// pausedTmux flag through unchanged; it never branches on row status itself.
func (s *Server) handleUpdateActiveStepContext(conn net.Conn, req UpdateActiveStepContextRequest) {
	if strings.TrimSpace(req.StepName) == "" {
		s.sendError(conn, "step_name is required")
		return
	}
	if req.TaskID <= 0 {
		s.sendError(conn, "task_id is required")
		return
	}

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "replace"
	}
	if mode != "replace" && mode != "append" {
		s.sendError(conn, fmt.Sprintf("invalid mode %q: must be \"replace\" or \"append\"", req.Mode))
		return
	}

	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get task #%d: %v", req.TaskID, err))
		return
	}

	pc, err := s.getProjectContext(t.ProjectID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get project context for task #%d: %v", req.TaskID, err))
		return
	}

	activeStep, pausedTmux := pc.engine.ResolveActiveStep(t)
	if activeStep == "" {
		s.sendError(conn, fmt.Sprintf("task #%d has no active step", req.TaskID))
		return
	}
	if activeStep != req.StepName {
		s.sendError(conn, fmt.Sprintf("step %q is not the active step (current: %q)", req.StepName, activeStep))
		return
	}

	rows, err := pc.engine.PublishManualStepContext(req.TaskID, req.StepName, req.Context, mode == "append", pausedTmux)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to update step context: %v", err))
		return
	}
	if rows == 0 {
		// The step matched but no task_steps row was updated — it either hasn't
		// started yet or its status no longer matches what we resolved.
		s.sendError(conn, fmt.Sprintf("step %q has no writable row for task #%d", req.StepName, req.TaskID))
		return
	}

	s.broadcastTaskUpdate(req.TaskID)
	s.sendMessage(conn, MsgOK, OKResponse{Message: fmt.Sprintf("step %q context updated (%s)", req.StepName, mode)})
}

func (s *Server) refineTaskTitle(taskID, projectID int64, branchName string, worktree bool, checkoutBranch string, description string, initialTitle string, manualTitle string) {
	projCfg := s.cfg
	if pc, err := s.getProjectContext(projectID); err == nil {
		projCfg = pc.cfg
	}

	var title string

	// Use manual title if provided, skipping AI generation
	if manualTitle != "" {
		title = manualTitle
	} else if description == "" {
		// Skip AI title generation when description is empty (existing branch with no prompt)
		title = initialTitle
	} else {
		ctx, cancel := context.WithTimeout(s.ctx, titleGenerationTimeout)
		defer cancel()

		var err error
		title, err = s.generateTitle(ctx, description, &projCfg.Claude)
		if err != nil {
			log.Printf("%sFailed to generate AI title for task #%d: %v", s.projectLogPrefix(projectID), taskID, err)
			if err := s.database.UpdateTaskStatus(taskID, task.StatusPending); err != nil {
				log.Printf("%sFailed to transition task #%d to pending: %v", s.projectLogPrefix(projectID), taskID, err)
			}
			s.broadcastTaskUpdate(taskID)
			return
		}
	}

	slug := task.Slugify(title)

	// Skip branch resolution for no-worktree tasks
	var branch string
	if worktree {
		if checkoutBranch != "" {
			branch = checkoutBranch
		} else {
			branch = projCfg.ResolveBranchForTask(taskID, title, slug, branchName)
		}
	}

	if err := s.database.FinalizeTaskIdentity(taskID, title, slug, branch); err != nil {
		log.Printf("%sFailed to update title for task #%d: %v", s.projectLogPrefix(projectID), taskID, err)
		if err := s.database.UpdateTaskStatus(taskID, task.StatusPending); err != nil {
			log.Printf("%sFailed to transition task #%d to pending: %v", s.projectLogPrefix(projectID), taskID, err)
		}
		s.broadcastTaskUpdate(taskID)
		return
	}

	if err := s.database.UpdateTaskStatus(taskID, task.StatusPending); err != nil {
		log.Printf("%sFailed to transition task #%d to pending: %v", s.projectLogPrefix(projectID), taskID, err)
		return
	}

	s.broadcastTaskUpdate(taskID)
	log.Printf("%sAI title for task #%d: %s (branch: %s)", s.projectLogPrefix(projectID), taskID, title, branch)
}

func (s *Server) generateTitle(ctx context.Context, description string, claude *config.ClaudeConfig) (string, error) {
	prompt := fmt.Sprintf(
		"Generate a concise task title (one short sentence, max 80 characters, no quotes, no prefix like 'Title:') for the following task description:\n\n%s",
		description,
	)

	args := []string{"-p", prompt, "--output-format", "text", "--model", "haiku"}
	args = append(args, claude.DefaultArgs...)

	cmd := exec.CommandContext(ctx, claude.Command, args...)
	cmd.Env = append(os.Environ(), "SORTIE_PURPOSE=title")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude command failed: %w (stderr: %s)", err, stderr.String())
	}

	title := task.SanitizeTitle(stdout.String())
	if title == "" {
		return "", fmt.Errorf("claude returned empty title")
	}

	return title, nil
}

// validateTaskRefs scans value for {{tasks.<id>.<field>}} references and
// classifies each by referenced-task status, returning a deduped list of
// referenced tasks that are currently active (and so should be merged into the
// task's BlockedBy edges). Returns an error for any of the disallowed cases:
//   - unsupported field name
//   - referenced task missing
//   - cross-project reference
//   - referenced task in failed or merge-failed status
//
// fieldLabel is used in error messages ("description" / "context") to identify
// where the offending reference lives. selfID is the ID of the task being
// validated (0 at create time, since no row exists yet); refs to selfID are
// never auto-added as blockers — a task cannot block itself.
func (s *Server) validateTaskRefs(value string, projectID, selfID int64, fieldLabel string) ([]int64, error) {
	refs := workflow.ExtractTaskRefs(value)
	if len(refs) == 0 {
		return nil, nil
	}
	if err := workflow.ValidateTaskRefs(refs); err != nil {
		return nil, err
	}

	seenAny := make(map[int64]bool) // avoid hitting the DB twice for repeated ids
	var autoBlockedBy []int64
	for _, r := range refs {
		if seenAny[r.ID] {
			continue
		}
		seenAny[r.ID] = true
		// A task referencing itself is fine (the lookup resolves it at runtime),
		// but it must never become its own blocker — that would deadlock the
		// claim filter on this task forever.
		if r.ID == selfID {
			continue
		}
		ref, err := s.database.GetTask(r.ID)
		if err != nil || ref == nil {
			return nil, fmt.Errorf("task #%d referenced in %s does not exist", r.ID, fieldLabel)
		}
		if ref.ProjectID != projectID {
			return nil, fmt.Errorf("task #%d referenced in %s belongs to another project", r.ID, fieldLabel)
		}
		switch {
		case ref.Status == task.StatusFailed, ref.Status == task.StatusMergeFailed:
			// Terminal without a landed result: auto-adding these as deps would
			// deadlock the referencing task forever, since GetClaimableTasks
			// only unblocks on a `completed` blocker.
			return nil, fmt.Errorf("task #%d referenced in %s is %s", r.ID, fieldLabel, ref.Status)
		case ref.Status == task.StatusCompleted:
			// Already resolved — value will be available at run time.
		default:
			// Active (pending, init, running, awaiting-approval, tmux,
			// finalizing, summarizing, merge-blocked) — auto-add as dep.
			autoBlockedBy = append(autoBlockedBy, r.ID)
		}
	}
	return autoBlockedBy, nil
}

// mergeBlockedBy returns the union of explicit and auto-collected BlockedBy
// task IDs, preserving the order of explicit IDs first.
func mergeBlockedBy(explicit, auto []int64) []int64 {
	if len(explicit) == 0 && len(auto) == 0 {
		return nil
	}
	seen := make(map[int64]bool, len(explicit)+len(auto))
	merged := make([]int64, 0, len(explicit)+len(auto))
	for _, id := range explicit {
		if seen[id] {
			continue
		}
		seen[id] = true
		merged = append(merged, id)
	}
	for _, id := range auto {
		if seen[id] {
			continue
		}
		seen[id] = true
		merged = append(merged, id)
	}
	return merged
}
