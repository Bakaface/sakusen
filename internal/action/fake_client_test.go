package action_test

import (
	"errors"

	"github.com/Bakaface/sortie/internal/daemon"
)

// fakeClient is the minimal stub used by verb tests. Every method returns the
// canned value/error stored on the struct; tests pre-load the field they need
// and assert on calls via the public counters.
type fakeClient struct {
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
}

var errNotStubbed = errors.New("fakeClient: method not stubbed")

func (f *fakeClient) StopTask(id int64) (*daemon.TaskInfo, error) {
	if f.stopTask == nil {
		return nil, errNotStubbed
	}
	return f.stopTask(id)
}

func (f *fakeClient) RetryTask(id int64, stepName string) (*daemon.TaskInfo, error) {
	if f.retryTask == nil {
		return nil, errNotStubbed
	}
	return f.retryTask(id, stepName)
}

func (f *fakeClient) RevertTask(id int64) (*daemon.TaskInfo, error) {
	if f.revertTask == nil {
		return nil, errNotStubbed
	}
	return f.revertTask(id)
}

func (f *fakeClient) DeleteTask(id int64) error {
	if f.deleteTask == nil {
		return errNotStubbed
	}
	return f.deleteTask(id)
}

func (f *fakeClient) ContinueTask(id int64, workflow, prompt string) (*daemon.TaskInfo, error) {
	if f.continueTask == nil {
		return nil, errNotStubbed
	}
	return f.continueTask(id, workflow, prompt)
}

func (f *fakeClient) CreateTaskWithOptions(req daemon.CreateTaskRequest) (*daemon.TaskInfo, error) {
	if f.createTask == nil {
		return nil, errNotStubbed
	}
	return f.createTask(req)
}

func (f *fakeClient) UpdateTaskField(id int64, field, value string) (*daemon.TaskInfo, error) {
	if f.updateField == nil {
		return nil, errNotStubbed
	}
	return f.updateField(id, field, value)
}

func (f *fakeClient) UpdateTaskPriority(id int64, priority string) (*daemon.TaskInfo, error) {
	if f.updatePriority == nil {
		return nil, errNotStubbed
	}
	return f.updatePriority(id, priority)
}

func (f *fakeClient) AttachBranch(id int64) (*daemon.TaskInfo, error) {
	if f.attachBranch == nil {
		return nil, errNotStubbed
	}
	return f.attachBranch(id)
}

func (f *fakeClient) DetachBranch(id int64) (*daemon.TaskInfo, error) {
	if f.detachBranch == nil {
		return nil, errNotStubbed
	}
	return f.detachBranch(id)
}

func (f *fakeClient) AddTaskDependency(taskID, blockedByID int64) (*daemon.TaskInfo, error) {
	if f.addDependency == nil {
		return nil, errNotStubbed
	}
	return f.addDependency(taskID, blockedByID)
}

func (f *fakeClient) RemoveTaskDependency(taskID, blockedByID int64) (*daemon.TaskInfo, error) {
	if f.rmDependency == nil {
		return nil, errNotStubbed
	}
	return f.rmDependency(taskID, blockedByID)
}

func (f *fakeClient) GetLogs(id int64, tail, offset int) ([]string, int, error) {
	if f.getLogs == nil {
		return nil, 0, errNotStubbed
	}
	return f.getLogs(id, tail, offset)
}

func (f *fakeClient) Cleanup(taskID int64) (int, []daemon.TaskInfo, error) {
	if f.cleanup == nil {
		return 0, nil, errNotStubbed
	}
	return f.cleanup(taskID)
}
