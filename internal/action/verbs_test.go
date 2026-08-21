package action_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/action"
	"github.com/Bakaface/sakusen/internal/daemon"
)

// Shared validation tests — exhaustive table for the single-int64 verbs to
// confirm the Validate() contract is uniform.
func TestSingleIDArgs_Validate(t *testing.T) {
	cases := []struct {
		name string
		args action.Args
	}{
		{"retry", action.RetryArgs{ID: 0}},
		{"revert", action.RevertArgs{ID: 0}},
		{"delete", action.DeleteArgs{ID: 0}},
		{"detach", action.DetachArgs{ID: 0}},
		{"attach-branch", action.AttachBranchArgs{ID: 0}},
		{"continue", action.ContinueArgs{ID: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.args.Validate(); err == nil {
				t.Fatalf("expected validation error for zero id")
			}
		})
	}
}

func TestRunRetry_Success(t *testing.T) {
	want := &daemon.TaskInfo{ID: 5, Status: "pending"}
	fc := &fakeClient{retryTask: func(int64, string) (*daemon.TaskInfo, error) { return want, nil }}
	res, err := action.RunRetry(action.Ctx{Client: fc}, action.RetryArgs{ID: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task != want {
		t.Fatal("Task not propagated")
	}
}

func TestRunDelete_NoTaskInResult(t *testing.T) {
	fc := &fakeClient{deleteTask: func(int64) error { return nil }}
	res, err := action.RunDelete(action.Ctx{Client: fc}, action.DeleteArgs{ID: 9})
	if err != nil {
		t.Fatal(err)
	}
	if res.Task != nil {
		t.Errorf("delete must not set Task, got %+v", res.Task)
	}
	if !strings.Contains(res.Message, "9") {
		t.Errorf("message should reference task id 9: %q", res.Message)
	}
}

func TestEditArgs_Validate(t *testing.T) {
	noField := action.EditArgs{ID: 1}
	if err := noField.Validate(); err == nil {
		t.Fatal("expected error when no field set")
	}
	bad := "bogus"
	badPri := action.EditArgs{ID: 1, Priority: &bad}
	if err := badPri.Validate(); err == nil {
		t.Fatal("expected error for invalid priority")
	}
	val := "high"
	ok := action.EditArgs{ID: 1, Priority: &val}
	if err := ok.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunEdit_AppliesAllFields(t *testing.T) {
	fieldCalls := []string{}
	prioCalls := 0
	fc := &fakeClient{
		updateField: func(_ int64, field, _ string) (*daemon.TaskInfo, error) {
			fieldCalls = append(fieldCalls, field)
			return &daemon.TaskInfo{ID: 1}, nil
		},
		updatePriority: func(_ int64, _ string) (*daemon.TaskInfo, error) {
			prioCalls++
			return &daemon.TaskInfo{ID: 1, Priority: "high"}, nil
		},
	}
	title, desc, ctxF, pri := "t", "d", "c", "high"
	args := action.EditArgs{ID: 1, Title: &title, Input: &desc, Context: &ctxF, Priority: &pri}
	res, err := action.RunEdit(action.Ctx{Client: fc}, args)
	if err != nil {
		t.Fatal(err)
	}
	if len(fieldCalls) != 3 {
		t.Errorf("expected 3 field updates, got %v", fieldCalls)
	}
	if prioCalls != 1 {
		t.Errorf("expected 1 priority update, got %d", prioCalls)
	}
	if res.Task == nil || res.Task.Priority != "high" {
		t.Errorf("expected last task to be the priority update, got %+v", res.Task)
	}
}

func TestRunEdit_AbortsOnError(t *testing.T) {
	sentinel := errors.New("nope")
	fc := &fakeClient{
		updateField: func(_ int64, _ string, _ string) (*daemon.TaskInfo, error) { return nil, sentinel },
	}
	title := "t"
	_, err := action.RunEdit(action.Ctx{Client: fc}, action.EditArgs{ID: 1, Title: &title})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel, got %v", err)
	}
}

func TestCreateArgs_Validate(t *testing.T) {
	if err := (action.CreateArgs{}).Validate(); err == nil {
		t.Fatal("expected error for empty project path")
	}
	if err := (action.CreateArgs{ProjectPath: "/x", Branch: "b", Checkout: "c"}).Validate(); err == nil {
		t.Fatal("expected error for branch + checkout")
	}
	if err := (action.CreateArgs{ProjectPath: "/x", Priority: "weird"}).Validate(); err == nil {
		t.Fatal("expected error for bad priority")
	}
	if err := (action.CreateArgs{ProjectPath: "/x"}).Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestDependsOnArgs_Validate(t *testing.T) {
	if err := (action.DependsOnArgs{TaskID: 1, BlockedByID: 1, Direction: "add"}).Validate(); err == nil {
		t.Fatal("expected self-dep error")
	}
	if err := (action.DependsOnArgs{TaskID: 1, BlockedByID: 2, Direction: "huh"}).Validate(); err == nil {
		t.Fatal("expected invalid direction error")
	}
	if err := (action.DependsOnArgs{TaskID: 1, BlockedByID: 2, Direction: "add"}).Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestCleanupArgs_Validate_AllowsZero(t *testing.T) {
	if err := (action.CleanupArgs{TaskID: 0}).Validate(); err != nil {
		t.Fatalf("zero must be allowed (means cleanup all): %v", err)
	}
}

func TestRunCleanup_FormatsMessage(t *testing.T) {
	fc := &fakeClient{cleanup: func(int64) (int, []daemon.TaskInfo, error) {
		return 3, []daemon.TaskInfo{{ID: 1}, {ID: 2}, {ID: 3}}, nil
	}}
	res, err := action.RunCleanup(action.Ctx{Client: fc}, action.CleanupArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 3 {
		t.Errorf("Count not propagated: %d", res.Count)
	}
	if len(res.Tasks) != 3 {
		t.Errorf("Tasks not propagated")
	}
}

func TestRunLogs_EmptyMessage(t *testing.T) {
	fc := &fakeClient{getLogs: func(int64, int, int) ([]string, int, error) { return nil, 0, nil }}
	res, err := action.RunLogs(action.Ctx{Client: fc}, action.LogsArgs{ID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Message, "No logs") {
		t.Errorf("unexpected empty message: %q", res.Message)
	}
}

func TestRegistry_HasAllVerbs(t *testing.T) {
	want := []string{
		"stop", "retry", "revert", "delete", "continue", "detach", "attach-branch",
		"depends-on", "edit", "create", "cleanup", "logs", "validate",
	}
	for _, v := range want {
		if _, ok := action.Registry[v]; !ok {
			t.Errorf("Registry missing %q", v)
		}
	}
	if len(action.Registry) != len(want) {
		t.Errorf("Registry has %d entries, expected %d", len(action.Registry), len(want))
	}
}
