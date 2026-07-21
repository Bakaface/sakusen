package workflow

import (
	"errors"
	"testing"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/db"
	"github.com/Bakaface/sortie/internal/task"
)

// stepContextWriteCall records a single invocation of one of the row-status-
// specific step-context writers, letting tests assert WHICH writer a given
// call routed to (see TestPublishManualStepContext_RowStatusRouting).
type stepContextWriteCall struct {
	taskID     int64
	stepName   string
	value      string
	appendMode bool
}

// fakeTaskStore is a test-only stand-in for *db.DB satisfying the Engine's
// taskStore seam, following the fakeGitRepo pattern in
// internal/merge/coordinator_test.go. It backs the handful of methods
// exercised by promoteSingleStepContextToTask and the stepcontext.go
// precedence/row-status tests with in-memory maps instead of a real SQLite
// database; everything else is stubbed to satisfy the interface since Go
// requires the full method set even when a given test only drives a couple
// of them.
type fakeTaskStore struct {
	stepContexts        map[int64]map[string]string   // taskID -> stepName -> context (completed row)
	runningStepContexts map[int64]map[string]string   // taskID -> stepName -> context (running row)
	taskContexts        map[int64]string              // records UpdateTaskContext calls
	chats               map[int64]map[string]*db.Chat // taskID -> stepName -> chat

	// Call logs for the row-status-specific writers — see PublishManualStepContext
	// (stepcontext.go), whose whole job is picking exactly one of these per call.
	updateRunningCalls []stepContextWriteCall
	updatePausedCalls  []stepContextWriteCall

	// trackChains backs GetTrackChain (keyed by leaf track ID, root-first).
	trackChains map[int64][]*task.Track

	getTaskStepContextErr error
	getTrackChainErr      error
}

func newFakeTaskStore() *fakeTaskStore {
	return &fakeTaskStore{
		stepContexts:        make(map[int64]map[string]string),
		runningStepContexts: make(map[int64]map[string]string),
		taskContexts:        make(map[int64]string),
		chats:               make(map[int64]map[string]*db.Chat),
		trackChains:         make(map[int64][]*task.Track),
	}
}

func (f *fakeTaskStore) GetTrackChain(id int64) ([]*task.Track, error) {
	if f.getTrackChainErr != nil {
		return nil, f.getTrackChainErr
	}
	if chain, ok := f.trackChains[id]; ok {
		return chain, nil
	}
	return nil, errors.New("track not found")
}

func (f *fakeTaskStore) GetTaskStepContext(taskID int64, stepName string) (string, error) {
	if f.getTaskStepContextErr != nil {
		return "", f.getTaskStepContextErr
	}
	return f.stepContexts[taskID][stepName], nil
}

func (f *fakeTaskStore) UpdateTaskContext(id int64, taskContext string) error {
	f.taskContexts[id] = taskContext
	return nil
}

// Unused by promoteSingleStepContextToTask; stubbed to satisfy taskStore.
func (f *fakeTaskStore) GetTask(id int64) (*task.Task, error)                { return nil, nil }
func (f *fakeTaskStore) UpdateTaskStatus(id int64, status task.Status) error { return nil }
func (f *fakeTaskStore) UpdateTaskBranch(id int64, branch string) error      { return nil }
func (f *fakeTaskStore) UpdateTaskWorktreePath(id int64, worktreePath string) error {
	return nil
}
func (f *fakeTaskStore) ClearWorktreePath(id int64) error { return nil }
func (f *fakeTaskStore) UpdateTaskStep(id int64, stepIndex int, currentStep string) error {
	return nil
}
func (f *fakeTaskStore) UpdateTaskExitCode(id int64, exitCode int, errorMessage string) error {
	return nil
}
func (f *fakeTaskStore) UpdateTaskLoopIteration(id int64, iteration int) error { return nil }
func (f *fakeTaskStore) AppendTaskCommit(id int64, commitHash string) error    { return nil }
func (f *fakeTaskStore) CreateTaskStep(taskID int64, stepName string) error    { return nil }
func (f *fakeTaskStore) CompleteTaskStep(taskID int64, stepName string, context *string, exitCode int) error {
	return nil
}
func (f *fakeTaskStore) UpdateTaskStepContext(taskID int64, stepName string, context string) error {
	return nil
}
func (f *fakeTaskStore) GetTaskStepContexts(taskID int64, stepNames []string) (map[string]string, error) {
	return nil, nil
}
func (f *fakeTaskStore) GetRunningTaskStepContext(taskID int64, stepName string) (string, error) {
	return f.runningStepContexts[taskID][stepName], nil
}
func (f *fakeTaskStore) UpdateRunningTaskStepContext(taskID int64, stepName, value string, appendMode bool) (int64, error) {
	f.updateRunningCalls = append(f.updateRunningCalls, stepContextWriteCall{taskID, stepName, value, appendMode})
	if f.runningStepContexts[taskID] == nil {
		f.runningStepContexts[taskID] = make(map[string]string)
	}
	f.runningStepContexts[taskID][stepName] = value
	return 1, nil
}
func (f *fakeTaskStore) UpdatePausedTmuxStepContext(taskID int64, stepName, value string, appendMode bool) (int64, error) {
	f.updatePausedCalls = append(f.updatePausedCalls, stepContextWriteCall{taskID, stepName, value, appendMode})
	if f.stepContexts[taskID] == nil {
		f.stepContexts[taskID] = make(map[string]string)
	}
	f.stepContexts[taskID][stepName] = value
	return 1, nil
}
func (f *fakeTaskStore) UpsertChat(taskID int64, stepName, sessionID, tmuxSessionName string) error {
	return nil
}
func (f *fakeTaskStore) GetChatByStep(taskID int64, stepName string) (*db.Chat, error) {
	return f.chats[taskID][stepName], nil
}
func (f *fakeTaskStore) SetChatSessionID(taskID int64, stepName, sessionID string) error {
	if f.chats[taskID] == nil {
		f.chats[taskID] = make(map[string]*db.Chat)
	}
	existing := f.chats[taskID][stepName]
	if existing == nil {
		existing = &db.Chat{TaskID: taskID, StepName: stepName}
		f.chats[taskID][stepName] = existing
	}
	existing.SessionID = sessionID
	return nil
}
func (f *fakeTaskStore) HasAnyWaitsOn(taskID int64) (bool, error)              { return false, nil }
func (f *fakeTaskStore) GetWaitsOnChildren(taskID int64) ([]*task.Task, error) { return nil, nil }
func (f *fakeTaskStore) RemoveAllTaskWaitsOn(taskID int64) error               { return nil }

// TestPromoteSingleStepContextToTaskFakeStore demonstrates the taskStore fake
// seam: promoteSingleStepContextToTask reads a step's captured context and
// writes it back as the task's context, entirely against an in-memory fake —
// no SQLite involved. Compare with TestPromoteSingleStepContextToTask (and
// its NoOp siblings) which drive the same method against a real database;
// those are left as-is per the migration-is-a-later-phase rule, but this one
// proves the Engine's DB dependency surface can be swapped out in a unit
// test.
func TestPromoteSingleStepContextToTaskFakeStore(t *testing.T) {
	wf := &config.WorkflowConfig{Steps: []config.StepConfig{{Name: "only"}}}

	t.Run("promotes trimmed step context and persists it", func(t *testing.T) {
		store := newFakeTaskStore()
		store.stepContexts[1] = map[string]string{"only": "  concise step summary text  "}
		e := &Engine{database: store}
		tk := &task.Task{ID: 1}

		if !e.promoteSingleStepContextToTask(tk, wf, nil) {
			t.Fatal("expected promotion to succeed for single-step workflow with non-empty step context")
		}
		const want = "concise step summary text"
		if tk.Context != want {
			t.Errorf("in-memory task.Context = %q, want %q", tk.Context, want)
		}
		if got := store.taskContexts[1]; got != want {
			t.Errorf("UpdateTaskContext persisted %q, want %q", got, want)
		}
	})

	t.Run("no-op when step context is empty", func(t *testing.T) {
		store := newFakeTaskStore()
		store.stepContexts[2] = map[string]string{"only": "   "}
		e := &Engine{database: store}
		tk := &task.Task{ID: 2}

		if e.promoteSingleStepContextToTask(tk, wf, nil) {
			t.Fatal("expected no promotion when step context is blank")
		}
		if _, wrote := store.taskContexts[2]; wrote {
			t.Error("UpdateTaskContext should not have been called")
		}
	})

	t.Run("no-op when the store read fails", func(t *testing.T) {
		store := newFakeTaskStore()
		store.getTaskStepContextErr = errors.New("simulated read failure")
		e := &Engine{database: store}
		tk := &task.Task{ID: 3}

		if e.promoteSingleStepContextToTask(tk, wf, nil) {
			t.Fatal("expected no promotion when GetTaskStepContext errors")
		}
	})
}
