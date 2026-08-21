package tui

import (
	"errors"

	"github.com/Bakaface/sakusen/internal/daemon"
)

// fakeTaskService is the TUI-side counterpart to internal/action's
// fakeClient test double: a minimal TaskService stub where every method
// returns the canned value stored on the struct, or errNotStubbedTUI if the
// test never set it. It exists so tests can construct a Model with a
// non-nil, fully-controllable client and actually EXECUTE the tea.Cmd
// returned by action factories / refreshTasks / loadOutput — not just
// assert that a cmd came back.
type fakeTaskService struct {
	stopTask       func(int64) (*daemon.TaskInfo, error)
	retryTask      func(int64, string) (*daemon.TaskInfo, error)
	revertTask     func(int64) (*daemon.TaskInfo, error)
	deleteTask     func(int64) error
	continueTask   func(int64, string, string) (*daemon.TaskInfo, error)
	createTask     func(daemon.CreateTaskRequest) (*daemon.TaskInfo, error)
	updateField    func(int64, string, string) (*daemon.TaskInfo, error)
	updatePriority func(int64, string) (*daemon.TaskInfo, error)
	attachBranch   func(int64) (*daemon.TaskInfo, error)
	detachBranch   func(int64) (*daemon.TaskInfo, error)
	addDependency  func(int64, int64) (*daemon.TaskInfo, error)
	rmDependency   func(int64, int64) (*daemon.TaskInfo, error)
	getLogs        func(int64, int, int) ([]string, int, error)
	cleanup        func(int64) (int, []daemon.TaskInfo, error)

	listTasksFiltered      func(int64) ([]daemon.TaskInfo, error)
	listTasksByProjectName func(string) ([]daemon.TaskInfo, error)
	getTaskSteps           func(int64) ([]daemon.TaskStepDetail, error)
	updateStepContext      func(int64, string, string) error
	advanceTask            func(int64) (string, error)
	connect                func() error
	subscribe              func() error
	messages               func() <-chan *daemon.Message
	close                  func() error
}

var errNotStubbedTUI = errors.New("fakeTaskService: method not stubbed")

func (f *fakeTaskService) StopTask(id int64) (*daemon.TaskInfo, error) {
	if f.stopTask == nil {
		return nil, errNotStubbedTUI
	}
	return f.stopTask(id)
}

func (f *fakeTaskService) RetryTask(id int64, stepName string) (*daemon.TaskInfo, error) {
	if f.retryTask == nil {
		return nil, errNotStubbedTUI
	}
	return f.retryTask(id, stepName)
}

func (f *fakeTaskService) RevertTask(id int64) (*daemon.TaskInfo, error) {
	if f.revertTask == nil {
		return nil, errNotStubbedTUI
	}
	return f.revertTask(id)
}

func (f *fakeTaskService) DeleteTask(id int64) error {
	if f.deleteTask == nil {
		return errNotStubbedTUI
	}
	return f.deleteTask(id)
}

func (f *fakeTaskService) ContinueTask(id int64, workflow, prompt string) (*daemon.TaskInfo, error) {
	if f.continueTask == nil {
		return nil, errNotStubbedTUI
	}
	return f.continueTask(id, workflow, prompt)
}

func (f *fakeTaskService) CreateTaskWithOptions(req daemon.CreateTaskRequest) (*daemon.TaskInfo, error) {
	if f.createTask == nil {
		return nil, errNotStubbedTUI
	}
	return f.createTask(req)
}

func (f *fakeTaskService) UpdateTaskField(id int64, field, value string) (*daemon.TaskInfo, error) {
	if f.updateField == nil {
		return nil, errNotStubbedTUI
	}
	return f.updateField(id, field, value)
}

func (f *fakeTaskService) UpdateTaskPriority(id int64, priority string) (*daemon.TaskInfo, error) {
	if f.updatePriority == nil {
		return nil, errNotStubbedTUI
	}
	return f.updatePriority(id, priority)
}

func (f *fakeTaskService) AttachBranch(id int64) (*daemon.TaskInfo, error) {
	if f.attachBranch == nil {
		return nil, errNotStubbedTUI
	}
	return f.attachBranch(id)
}

func (f *fakeTaskService) DetachBranch(id int64) (*daemon.TaskInfo, error) {
	if f.detachBranch == nil {
		return nil, errNotStubbedTUI
	}
	return f.detachBranch(id)
}

func (f *fakeTaskService) AddTaskDependency(taskID, blockedByID int64) (*daemon.TaskInfo, error) {
	if f.addDependency == nil {
		return nil, errNotStubbedTUI
	}
	return f.addDependency(taskID, blockedByID)
}

func (f *fakeTaskService) RemoveTaskDependency(taskID, blockedByID int64) (*daemon.TaskInfo, error) {
	if f.rmDependency == nil {
		return nil, errNotStubbedTUI
	}
	return f.rmDependency(taskID, blockedByID)
}

func (f *fakeTaskService) GetLogs(id int64, tail, offset int) ([]string, int, error) {
	if f.getLogs == nil {
		return nil, 0, errNotStubbedTUI
	}
	return f.getLogs(id, tail, offset)
}

func (f *fakeTaskService) Cleanup(taskID int64) (int, []daemon.TaskInfo, error) {
	if f.cleanup == nil {
		return 0, nil, errNotStubbedTUI
	}
	return f.cleanup(taskID)
}

func (f *fakeTaskService) ListTasksFiltered(projectID int64) ([]daemon.TaskInfo, error) {
	if f.listTasksFiltered == nil {
		return nil, errNotStubbedTUI
	}
	return f.listTasksFiltered(projectID)
}

func (f *fakeTaskService) ListTasksByProjectName(name string) ([]daemon.TaskInfo, error) {
	if f.listTasksByProjectName == nil {
		return nil, errNotStubbedTUI
	}
	return f.listTasksByProjectName(name)
}

func (f *fakeTaskService) GetTaskSteps(taskID int64) ([]daemon.TaskStepDetail, error) {
	if f.getTaskSteps == nil {
		return nil, errNotStubbedTUI
	}
	return f.getTaskSteps(taskID)
}

func (f *fakeTaskService) UpdateStepContext(taskID int64, stepName, context string) error {
	if f.updateStepContext == nil {
		return errNotStubbedTUI
	}
	return f.updateStepContext(taskID, stepName, context)
}

func (f *fakeTaskService) AdvanceTask(id int64) (string, error) {
	if f.advanceTask == nil {
		return "", errNotStubbedTUI
	}
	return f.advanceTask(id)
}

func (f *fakeTaskService) Connect() error {
	if f.connect == nil {
		return nil
	}
	return f.connect()
}

func (f *fakeTaskService) Subscribe() error {
	if f.subscribe == nil {
		return nil
	}
	return f.subscribe()
}

func (f *fakeTaskService) Messages() <-chan *daemon.Message {
	if f.messages == nil {
		ch := make(chan *daemon.Message)
		close(ch)
		return ch
	}
	return f.messages()
}

func (f *fakeTaskService) Close() error {
	if f.close == nil {
		return nil
	}
	return f.close()
}
