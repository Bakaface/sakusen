// Package action centralizes the 13 user-facing task verbs that Sortie's
// three surfaces (Cobra CLI, TUI palette, TUI keybindings) share. Each verb
// has a typed Args struct, a Validate() method, and a Run<Verb> entry point
// that returns a uniform Result. The CLI and TUI become thin adapters that
// build a Ctx and dispatch to the verb; argument-shaping, validation, and
// daemon-RPC composition live here, not in each surface.
package action

import (
	"io"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/daemon"
)

// Ctx is the carrier struct every action receives. CLI builds one from
// package-globals; TUI builds one from Model fields (m.cfg, m.client).
type Ctx struct {
	Cfg    *config.Config
	Client ClientAPI
	Out    io.Writer // CLI prints here; TUI passes io.Discard and reads Result.Message
}

// ClientAPI is the narrow daemon-facing interface actions depend on.
// *client.Client satisfies it implicitly. Tests pass a hand-rolled fake.
// The method set is the union of every daemon call the 13 verbs need.
type ClientAPI interface {
	StopTask(id int64) (*daemon.TaskInfo, error)
	RetryTask(id int64, stepName string) (*daemon.TaskInfo, error)
	RevertTask(id int64) (*daemon.TaskInfo, error)
	DeleteTask(id int64) error
	ContinueTask(id int64, workflow, prompt string) (*daemon.TaskInfo, error)
	CreateTaskWithOptions(req daemon.CreateTaskRequest) (*daemon.TaskInfo, error)
	UpdateTaskField(id int64, field, value string) (*daemon.TaskInfo, error)
	UpdateTaskPriority(id int64, priority string) (*daemon.TaskInfo, error)
	AttachBranch(id int64) (*daemon.TaskInfo, error)
	DetachBranch(id int64) (*daemon.TaskInfo, error)
	AddTaskDependency(taskID, blockedByID int64) (*daemon.TaskInfo, error)
	RemoveTaskDependency(taskID, blockedByID int64) (*daemon.TaskInfo, error)
	GetLogs(id int64, tail, offset int) ([]string, int, error)
	Cleanup(taskID int64) (int, []daemon.TaskInfo, error)
}

// Result is the shared return shape. Each action populates only the fields it
// actually produces — Message is always set; Task is set when a single task
// is mutated; Tasks/Count are set by batch verbs like cleanup.
type Result struct {
	Message string
	Task    *daemon.TaskInfo
	Tasks   []daemon.TaskInfo
	Count   int
}

// Args is implemented by every per-verb args struct so the Registry can
// type-erase. Concrete callers (CLI/TUI keys) use the typed entry points
// (e.g. action.RunStop) and never go through this interface.
type Args interface {
	Validate() error
}

// Action is the registry entry. Typed entry points live alongside as
// package-level funcs (e.g. RunStop) for CLI/TUI key dispatch.
type Action struct {
	ID    string                             // kebab-case, matches CLI subcommand
	Help  string                             // one-liner for palette + cobra
	Run   func(Ctx, Args) (Result, error)    // type-erased — used by palette only
	Parse func(rawArgs string) (Args, error) // palette path: parse free-text args
}
