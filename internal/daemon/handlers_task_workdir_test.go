package daemon

import (
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
)

// Workdir precedence: explicit request > workflow pin > checkout root; the
// value is persisted on the task row.
func TestCreateTaskFromRequest_Workdir(t *testing.T) {
	pinned := config.WorkflowConfig{
		Name:    "pinned",
		Input:   "Do the thing",
		Workdir: "em/rails-foundation",
		Steps:   []config.StepConfig{{Name: "implement"}},
	}
	plain := config.WorkflowConfig{
		Name:  "pinned",
		Input: "Do the thing",
		Steps: []config.StepConfig{{Name: "implement"}},
	}

	tests := []struct {
		name string
		wf   config.WorkflowConfig
		req  string
		want string
	}{
		{name: "pin applies when request omits it", wf: pinned, want: "em/rails-foundation"},
		{name: "request beats pin", wf: pinned, req: " ./personal/georgia-residency/ ", want: "personal/georgia-residency"},
		{name: "neither leaves it empty", wf: plain, want: ""},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := "/tmp/sakusen-workdir-test-" + string(rune('a'+i))
			s, _ := setupServerWithPinnedWorkflow(t, path, tt.wf)
			tk, _, err := s.createTaskFromRequest(CreateTaskRequest{
				ProjectPath: path,
				Workflow:    "pinned",
				Workdir:     tt.req,
			})
			if err != nil {
				t.Fatalf("createTaskFromRequest: %v", err)
			}
			if tk.Workdir != tt.want {
				t.Errorf("returned Workdir = %q, want %q", tk.Workdir, tt.want)
			}
			stored, err := s.database.GetTask(tk.ID)
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if stored.Workdir != tt.want {
				t.Errorf("stored Workdir = %q, want %q", stored.Workdir, tt.want)
			}
			if info := s.taskToInfo(stored); info.Workdir != tt.want {
				t.Errorf("TaskInfo.Workdir = %q, want %q", info.Workdir, tt.want)
			}
		})
	}
}

func TestCreateTaskFromRequest_InvalidWorkdirRejected(t *testing.T) {
	wf := config.WorkflowConfig{
		Name:  "pinned",
		Input: "Do the thing",
		Steps: []config.StepConfig{{Name: "implement"}},
	}
	for i, bad := range []string{"/abs", "../x", "a/../../x"} {
		path := "/tmp/sakusen-workdir-bad-" + string(rune('a'+i))
		s, _ := setupServerWithPinnedWorkflow(t, path, wf)
		_, _, err := s.createTaskFromRequest(CreateTaskRequest{
			ProjectPath: path,
			Workflow:    "pinned",
			Workdir:     bad,
		})
		if err == nil || !strings.Contains(err.Error(), "workdir") {
			t.Errorf("Workdir %q: expected workdir error, got %v", bad, err)
		}
	}
}
