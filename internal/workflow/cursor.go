package workflow

import (
	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/task"
)

// Step cursor invariant.
//
// t.StepIndex is the index into wf.Steps of the NEXT step RunTask will
// execute (or resume at). This holds for every way a task reaches RunTask:
//   - fresh task: StepIndex is 0.
//   - loop-back (step.Loop.Goto): StepIndex is set to the target step's index.
//   - retry-from-step (failed task): StepIndex is left untouched at the index
//     of the step that failed — RunTask re-runs it.
//   - resume after a paused approval/tmux gate (ResumeAfterApproval): StepIndex
//     already points at the step to run next; see below.
//
// The one non-obvious case: when a step requires human approval or runs in
// tmux, RunTask bumps StepIndex to i+1 BEFORE pausing (see the needsApproval
// branch), so a task paused at a gate has StepIndex pointing PAST the step
// that just ran / owns the paused session. That step is therefore at
// StepIndex-1, not StepIndex. PausedStep and HasMoreSteps below are the only
// sanctioned way to work with that arithmetic — callers outside this package
// must not re-derive it.

// PausedStep returns the step a gate-paused task (awaiting-approval or tmux
// status) is sitting on — the step that just ran and owns the paused
// session/approval — along with whether that index is in range. It is the
// StepIndex-1 step per the cursor invariant above.
func PausedStep(t *task.Task, wf *config.WorkflowConfig) (config.StepConfig, bool) {
	if wf == nil {
		return config.StepConfig{}, false
	}
	idx := t.StepIndex - 1
	if idx < 0 || idx >= len(wf.Steps) {
		return config.StepConfig{}, false
	}
	return wf.Steps[idx], true
}

// HasMoreSteps reports whether steps remain to run after the one a
// gate-paused task is sitting on (see PausedStep). Equivalent to
// "t.StepIndex is still a valid index into wf.Steps".
func HasMoreSteps(t *task.Task, wf *config.WorkflowConfig) bool {
	if wf == nil {
		return false
	}
	return t.StepIndex < len(wf.Steps)
}
