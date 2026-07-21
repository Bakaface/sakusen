package workflow

import (
	"github.com/Bakaface/sortie/internal/db"
	"github.com/Bakaface/sortie/internal/task"
)

// taskStore is the narrow slice of *db.DB that Engine actually depends on.
// Before this interface existed, Engine held a concrete *db.DB and any file
// in this package could reach for any of the ~50 methods that type exposes —
// the true dependency surface was invisible. This interface writes that
// surface down in one place and lets tests substitute a hand-rolled fake
// instead of opening a real SQLite database (see the gitRepo seam in
// internal/merge for the same pattern applied to git operations).
//
// *db.DB satisfies this implicitly, so callers (the daemon, tests, cmd/sortie)
// keep passing a *db.DB into NewEngine unchanged.
//
// Methods are grouped by concern, mirroring the order they're touched during
// a task run: task reads/writes, step tracking, chat sessions, then
// child-task suspension (task_waits_on).
type taskStore interface {
	// Task reads and field updates.
	GetTask(id int64) (*task.Task, error)
	UpdateTaskStatus(id int64, status task.Status) error
	UpdateTaskBranch(id int64, branch string) error
	UpdateTaskWorktreePath(id int64, worktreePath string) error
	ClearWorktreePath(id int64) error
	UpdateTaskStep(id int64, stepIndex int, currentStep string) error
	UpdateTaskExitCode(id int64, exitCode int, errorMessage string) error
	UpdateTaskContext(id int64, taskContext string) error
	UpdateTaskLoopIteration(id int64, iteration int) error
	AppendTaskCommit(id int64, commitHash string) error

	// Per-step execution tracking (task_steps table).
	CreateTaskStep(taskID int64, stepName string) error
	CompleteTaskStep(taskID int64, stepName string, context *string, exitCode int) error
	UpdateTaskStepContext(taskID int64, stepName string, context string) error
	GetTaskStepContext(taskID int64, stepName string) (string, error)
	GetTaskStepContexts(taskID int64, stepNames []string) (map[string]string, error)
	GetRunningTaskStepContext(taskID int64, stepName string) (string, error)
	// UpdateRunningTaskStepContext and UpdatePausedTmuxStepContext are the two
	// row-status-specific writers behind PublishManualStepContext (see
	// stepcontext.go) — the sole caller of both. Nothing outside this package
	// should call them directly; the daemon routes update_step_context writes
	// through the Engine instead.
	UpdateRunningTaskStepContext(taskID int64, stepName, value string, appendMode bool) (int64, error)
	UpdatePausedTmuxStepContext(taskID int64, stepName, value string, appendMode bool) (int64, error)

	// Chat session tracking (tmux steps).
	UpsertChat(taskID int64, stepName, sessionID, tmuxSessionName string) error
	GetChatByStep(taskID int64, stepName string) (*db.Chat, error)
	// SetChatSessionID is used by RecordTmuxStepSentinelSession (stepcontext.go)
	// to correct the recorded session id from the Stop-hook sentinel payload.
	SetChatSessionID(taskID int64, stepName, sessionID string) error

	// Track chain reads (tracks table). GetTrackChain returns the track and
	// its ancestors root-first; the engine reads it fresh at each step launch
	// so mid-run track context updates are visible to later steps.
	GetTrackChain(id int64) ([]*task.Track, error)

	// Mid-step child-task suspension (task_waits_on table).
	HasAnyWaitsOn(taskID int64) (bool, error)
	GetWaitsOnChildren(taskID int64) ([]*task.Task, error)
	RemoveAllTaskWaitsOn(taskID int64) error
}
