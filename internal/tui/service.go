package tui

import (
	"github.com/Bakaface/sakusen/internal/action"
	"github.com/Bakaface/sakusen/internal/daemon"
)

// TaskService is the complete daemon-facing surface the TUI depends on. It
// embeds action.ClientAPI — the 13 user-facing verbs shared with the CLI and
// the TUI command palette — and adds the TUI-only reads and
// connection-lifecycle calls that never went through the action package:
// task listing for the list view, log streaming, step-context reads, and
// pub/sub connection setup.
//
// *client.Client satisfies this interface with no changes to internal/client
// (every method it needs already exists there). Model.client is typed as
// TaskService (not the concrete client) so tests can substitute a fake and
// actually execute the tea.Cmds the TUI's action factories return, instead
// of only asserting that a non-nil cmd came back.
type TaskService interface {
	action.ClientAPI

	// Task listing — used by the list view's initial load and refresh poll.
	ListTasksFiltered(projectID int64) ([]daemon.TaskInfo, error)
	ListTasksByProjectName(name string) ([]daemon.TaskInfo, error)

	// Step context reads/writes — artifact viewer and step editor.
	GetTaskSteps(taskID int64) ([]daemon.TaskStepDetail, error)
	UpdateStepContext(taskID int64, stepName, context string) error

	// Tmux-gate completion. Not modeled as an action.Result-returning verb
	// because the TUI doesn't need the returned TaskInfo (a refresh poll
	// picks up the new state).
	AdvanceTask(id int64) (string, error)

	// Connection lifecycle.
	Connect() error
	Subscribe() error
	Messages() <-chan *daemon.Message
	Close() error
}
