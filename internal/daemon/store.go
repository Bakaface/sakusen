package daemon

import (
	"github.com/Bakaface/sortie/internal/db"
	"github.com/Bakaface/sortie/internal/task"
)

// taskStore is the slice of *db.DB that Server depends on. It exists for the
// same reason as workflow.taskStore and merge's gitRepo: before this
// interface existed, Server held a concrete *db.DB and any handler file could
// reach for any method that type exposes — the true dependency surface was
// invisible, scattered across server.go and handlers_*.go. This interface
// writes that surface down in one place and gives tests a fake-point.
//
// *db.DB satisfies this implicitly, so NewServer's callers keep passing a
// *db.DB unchanged.
//
// It is intentionally large (~50 methods) — Server is the daemon's single
// persistence-facing component and touches nearly all of db.DB's surface.
// Splitting it into several narrower interfaces threaded separately through
// Server would scatter that surface right back across files instead of
// documenting it; grouping by concern below keeps it readable as one seam.
//
// The "forwarded to workflow.Engine" group below is not called by Server
// directly — Server hands s.database to workflow.NewEngine as-is (see
// getProjectContext), so this interface must be a structural superset of
// workflow.taskStore for that call to typecheck. They're separated out so
// the "Server's own usage" groups above stay an accurate map of what the
// daemon package itself touches.
type taskStore interface {
	// Lifecycle.
	Close() error

	// Task CRUD, lookup, and lifecycle transitions.
	CreateTask(projectID int64, title, description, slug, workflow, branch string, status task.Status, images []string) (*task.Task, error)
	CreateTaskWithPriority(projectID int64, title, description, slug, workflow, branchName, branch, targetBranch, checkoutBranch string, status task.Status, priority task.Priority, worktree bool, images []string, trackID *int64) (*task.Task, error)
	GetTask(id int64) (*task.Task, error)
	GetAllTasks() ([]*task.Task, error)
	GetRunningTasks() ([]*task.Task, error)
	GetClaimableTasks() ([]*task.Task, error)
	GetTasksByProject(projectID int64) ([]*task.Task, error)
	GetTasksByProjectName(name string) ([]*task.Task, error)
	ClaimTask(id int64) (bool, error)
	DeleteTask(id int64) error
	ResetTaskForRetry(id int64) error
	ResetTaskForRetryFromStep(id int64) error
	ResetTaskForRetryAtStep(id int64, stepIdx int, stepNames []string) error
	ResetTaskForContinue(id int64, workflow, prompt string) error

	// Task field updates.
	UpdateTaskStatus(id int64, status task.Status) error
	UpdateTaskWorktreePath(id int64, worktreePath string) error
	ClearWorktreePath(id int64) error
	UpdateTaskBranch(id int64, branch string) error
	UpdateTaskStep(id int64, stepIndex int, currentStep string) error
	UpdateTaskError(id int64, errMsg string) error
	UpdateTaskStatusError(id int64, status task.Status, errMsg string) error
	UpdateTaskPriority(id int64, priority task.Priority) error
	UpdateTaskContext(id int64, taskContext string) error
	UpdateTaskTitle(id int64, title string) error
	UpdateTaskDescription(id int64, description string) error
	FinalizeTaskIdentity(id int64, title, slug, branch string) error
	SetWorktreeDetached(id int64, detached bool) error
	GetTaskCommits(id int64) ([]string, error)

	// Task dependencies (task_dependencies table).
	AddTaskDependency(taskID, blockedByID int64) error
	RemoveTaskDependency(taskID, blockedByID int64) error
	SetTaskDependencies(taskID int64, blockedBy []int64) error
	HasCircularDependency(taskID, newBlockedByID int64) (bool, error)

	// Mid-step child-task suspension (task_waits_on table).
	AddTaskWaitsOn(taskID, waitsOnID int64) error
	GetTaskWaitsOn(taskID int64) ([]int64, error)
	AllWaitsOnTerminal(taskID int64) (bool, error)
	GetTasksAwaitingChildren() ([]*task.Task, error)
	HasCircularWaitsOn(taskID, newWaitsOnID int64) (bool, error)

	// Tracks (tracks table). GetTrackChain also appears in the "forwarded to
	// workflow.Engine" group's contract — it is listed here because handlers
	// (get_track) call it directly too.
	CreateTrack(projectID *int64, name, slug, workflow, context, description string, parentID *int64) (*task.Track, error)
	GetTrack(id int64) (*task.Track, error)
	GetTrackBySlug(projectID *int64, slug string) (*task.Track, error)
	ListTracks(projectID int64) ([]*task.Track, error)
	UpdateTrackContext(id int64, context, mode string) error
	UpdateTrackDescription(id int64, description string) error
	GetTrackChain(id int64) ([]*task.Track, error)

	// Projects.
	GetOrCreateProject(projectPath string) (*db.Project, error)
	GetProject(id int64) (*db.Project, error)
	UpdateProjectDefaultWorktree(id int64, worktree bool) error
	UpdateProjectDefaults(id int64, worktree bool, branchMode int, workflow string) error

	// Per-step execution tracking (task_steps table). Note: manual
	// step-context writes (the update_step_context MCP tool) do NOT call
	// UpdateRunningTaskStepContext / UpdatePausedTmuxStepContext here — those
	// two live in the "forwarded to workflow.Engine" group below and are only
	// reachable through workflow.Engine.PublishManualStepContext, which owns
	// the running-vs-paused row-status decision (see stepcontext.go). The
	// daemon itself has no business knowing which task_steps row status a
	// write targets.
	CreateTaskStep(taskID int64, stepName string) error
	CompleteTaskStep(taskID int64, stepName string, context *string, exitCode int) error
	UpdateTaskStepContext(taskID int64, stepName string, context string) error
	GetAllTaskStepContexts(taskID int64) (map[string]string, error)
	GetTaskStepRows(taskID int64) (map[string]db.TaskStepRow, error)

	// Chat session tracking (tmux steps). SetChatSessionID is likewise absent
	// here (see the "forwarded" group below): correcting a tmux step's
	// recorded session from its Stop-hook sentinel routes through
	// workflow.Engine.RecordTmuxStepSentinelSession (see tmux_monitor.go).
	UpsertChat(taskID int64, stepName, sessionID, tmuxSessionName string) error
	GetLatestChat(taskID int64) (*db.Chat, error)
	GetChatByStep(taskID int64, stepName string) (*db.Chat, error)

	// Forwarded to workflow.Engine (via workflow.NewEngine(cfg, s.database, ...)
	// in getProjectContext) — see doc comment above. UpdateRunningTaskStepContext,
	// UpdatePausedTmuxStepContext, and SetChatSessionID land here rather than in
	// the "Server's own usage" groups above precisely because the daemon itself
	// never calls them directly anymore — they exist on this interface solely so
	// *db.DB (via s.database) satisfies workflow.taskStore.
	UpdateTaskExitCode(id int64, exitCode int, errorMessage string) error
	UpdateTaskLoopIteration(id int64, iteration int) error
	GetTaskStepContext(taskID int64, stepName string) (string, error)
	GetTaskStepContexts(taskID int64, stepNames []string) (map[string]string, error)
	GetRunningTaskStepContext(taskID int64, stepName string) (string, error)
	UpdateRunningTaskStepContext(taskID int64, stepName, value string, appendMode bool) (int64, error)
	UpdatePausedTmuxStepContext(taskID int64, stepName, value string, appendMode bool) (int64, error)
	SetChatSessionID(taskID int64, stepName, sessionID string) error
	HasAnyWaitsOn(taskID int64) (bool, error)
	GetWaitsOnChildren(taskID int64) ([]*task.Task, error)
	RemoveAllTaskWaitsOn(taskID int64) error
	AppendTaskCommit(id int64, commitHash string) error
}
