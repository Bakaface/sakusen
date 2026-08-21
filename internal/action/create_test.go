package action_test

import (
	"testing"

	"github.com/Bakaface/sakusen/internal/action"
	"github.com/Bakaface/sakusen/internal/daemon"
)

// TestRunCreate_WorktreePassthrough locks in the precedence-critical contract:
// RunCreate forwards args.Worktree to the daemon verbatim. A nil value must stay
// nil so the daemon can defer to the workflow's worktree pin (or project
// default); a non-nil value must reach the request as an explicit override.
// Sending an unsolicited explicit value here would clobber a worktree:false pin.
func TestRunCreate_WorktreePassthrough(t *testing.T) {
	tru := true
	fls := false
	cases := []struct {
		name string
		in   *bool
	}{
		{"unset defers to pin/default", nil},
		{"explicit true", &tru},
		{"explicit false", &fls},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got daemon.CreateTaskRequest
			fc := &fakeClient{createTask: func(req daemon.CreateTaskRequest) (*daemon.TaskInfo, error) {
				got = req
				return &daemon.TaskInfo{ID: 1}, nil
			}}

			_, err := action.RunCreate(action.Ctx{Client: fc}, action.CreateArgs{
				Input:       "do the thing",
				ProjectPath: "/tmp/proj",
				Worktree:    tc.in,
			})
			if err != nil {
				t.Fatalf("RunCreate: %v", err)
			}

			switch {
			case tc.in == nil && got.Worktree != nil:
				t.Errorf("Worktree: got %v, want nil (must defer to pin/default)", *got.Worktree)
			case tc.in != nil && got.Worktree == nil:
				t.Errorf("Worktree: got nil, want %v (explicit choice must pass through)", *tc.in)
			case tc.in != nil && got.Worktree != nil && *got.Worktree != *tc.in:
				t.Errorf("Worktree: got %v, want %v", *got.Worktree, *tc.in)
			}
		})
	}
}
