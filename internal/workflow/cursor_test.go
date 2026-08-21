package workflow

import (
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/task"
)

func threeStepWorkflow() *config.WorkflowConfig {
	return &config.WorkflowConfig{
		Name: "wf",
		Steps: []config.StepConfig{
			{Name: "first"},
			{Name: "second", Human: true},
			{Name: "third"},
		},
	}
}

func TestPausedStep(t *testing.T) {
	wf := threeStepWorkflow()

	tests := []struct {
		name      string
		stepIndex int
		wantStep  string
		wantOK    bool
	}{
		{"paused at first step (StepIndex=1)", 1, "first", true},
		{"paused mid-workflow (StepIndex=2)", 2, "second", true},
		{"paused at last step (StepIndex=3)", 3, "third", true},
		{"fresh task, StepIndex=0, nothing paused", 0, "", false},
		{"StepIndex out of range (past all steps)", 4, "", false},
		{"negative StepIndex", -1, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tk := &task.Task{StepIndex: tc.stepIndex}
			step, ok := PausedStep(tk, wf)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && step.Name != tc.wantStep {
				t.Fatalf("step.Name = %q, want %q", step.Name, tc.wantStep)
			}
		})
	}
}

func TestPausedStep_NilWorkflow(t *testing.T) {
	tk := &task.Task{StepIndex: 1}
	step, ok := PausedStep(tk, nil)
	if ok {
		t.Fatalf("expected ok=false for nil workflow, got step %+v", step)
	}
}

func TestHasMoreSteps(t *testing.T) {
	wf := threeStepWorkflow()

	tests := []struct {
		name      string
		stepIndex int
		want      bool
	}{
		{"paused at first step, two more remain (StepIndex=1)", 1, true},
		{"paused mid-workflow, one more remains (StepIndex=2)", 2, true},
		{"paused at last step, none remain (StepIndex=3)", 3, false},
		{"fresh task, StepIndex=0, all steps remain", 0, true},
		{"StepIndex past all steps", 4, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tk := &task.Task{StepIndex: tc.stepIndex}
			if got := HasMoreSteps(tk, wf); got != tc.want {
				t.Fatalf("HasMoreSteps = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHasMoreSteps_NilWorkflow(t *testing.T) {
	tk := &task.Task{StepIndex: 0}
	if HasMoreSteps(tk, nil) {
		t.Fatal("expected false for nil workflow")
	}
}

// TestPausedStep_RetryFromStepConvention documents that a failed task's
// StepIndex is left pointing at the step that failed (not paused past it —
// ResetTaskForRetryFromStep does not touch step_index), so PausedStep is not
// the right accessor for retry: the failed step is at StepIndex, not
// StepIndex-1. RunTask itself re-runs from t.StepIndex directly.
func TestPausedStep_RetryFromStepConvention(t *testing.T) {
	wf := threeStepWorkflow()
	// Task failed while running the second step (index 1); StepIndex was
	// never advanced past it since the failure short-circuited before the
	// needsApproval/UpdateTaskStep(i+1) bump.
	tk := &task.Task{StepIndex: 1}

	// PausedStep(t, wf) would resolve to the FIRST step (index 0), not the
	// failed step — confirming callers must not use PausedStep for the
	// retry/failed convention.
	step, ok := PausedStep(tk, wf)
	if !ok || step.Name != "first" {
		t.Fatalf("PausedStep on a failed task's StepIndex resolves to %q (ok=%v); retry must instead use StepIndex directly (%q)", step.Name, ok, wf.Steps[tk.StepIndex].Name)
	}
}
