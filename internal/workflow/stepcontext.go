package workflow

import (
	"context"
	"log"
	"strings"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/task"
)

// STEP-CONTEXT LIFECYCLE
//
// A workflow step's output becomes {{steps.<name>.context}} for later steps,
// and eventually the final task summary, via one of three capture strategies,
// consulted in strict precedence order:
//
//  1. MANUAL — an agent (or a human) pushed a value through the
//     update_step_context MCP tool while the step's session was live. A
//     manual write always wins; automatic capture is skipped entirely.
//  2. LAST_MESSAGE — the agent's result text from a headless step (read from
//     $SORTIE_RESULT_FILE, stdout-tail fallback). This is the synchronous
//     fallback captured the instant the step finishes, before any
//     summarization runs.
//  3. SUMMARIZE_CHAT — a summary of the step's full chat content produced by
//     the configured `summarizer:` command (headless step log region, or the
//     tmux agent's chat_log_command output). Used when the step's
//     `summarization_strategy` calls for it (the default) and the chat is
//     non-trivial (see shouldSummarizeChat in summarizer.go).
//
// Where each strategy is decided:
//   - Headless steps (and the tmux fire-and-forget spawn, best-effort) decide
//     synchronously inside RunTask via captureHeadlessStepContext below,
//     right after the step's agent command returns.
//   - Tmux/human steps generally cannot summarize synchronously — RunTask
//     returns immediately to pause at the approval gate, before the chat
//     exists — so summarizePreviousTmuxStep (summarizer.go) captures on the
//     NEXT engine entry point (ResumeAfterApproval or FinalizeTask).
//   - Single-step workflows additionally promote the one step's already-
//     captured context directly into task.context
//     (promoteSingleStepContextToTask, summarizer.go), skipping the
//     cross-step task summarizer. That promotion reads the final, already-
//     decided value (GetTaskStepContext) — it does not re-derive precedence.
//
// ROW-STATUS ROUTING
//
// A task_steps row moves running -> completed as its step executes. A manual
// write (via update_step_context) must target whichever row is actually live
// at write time:
//   - A step still executing (headless mid-run) has a 'running' row.
//   - A tmux/human step paused at its approval gate has ALREADY been flipped
//     to 'completed' the instant the engine spawned its session (so the task
//     could pause) — even though the interactive agent inside it is still
//     live and may want to fold its chat into the context.
//
// readManualOverride and PublishManualStepContext below are the only two
// places that encode this running-vs-completed branch. Every other step-
// context reader in this codebase either goes through one of them, or reads
// the final, already-decided value via GetTaskStepContext(s)
// (unconditionally the 'completed' row — the value later steps/the summarizer
// template in).

// readManualOverride reads whichever task_steps row currently represents the
// manual-override channel for stepName's context (see ROW-STATUS ROUTING
// above). pausedTmux selects the row: false checks the RUNNING row (used by
// captureHeadlessStepContext to detect a write pushed mid-step, before
// CompleteTaskStep flips the row to 'completed'); true checks the COMPLETED
// row (used by summarizePreviousTmuxStep to detect a chat manually folded
// into a tmux step already paused at its approval gate). has reports whether
// the read value is non-blank; it is only meaningful when err is nil.
func (e *Engine) readManualOverride(taskID int64, stepName string, pausedTmux bool) (value string, has bool, err error) {
	if pausedTmux {
		value, err = e.database.GetTaskStepContext(taskID, stepName)
	} else {
		value, err = e.database.GetRunningTaskStepContext(taskID, stepName)
	}
	if err != nil {
		return "", false, err
	}
	return value, strings.TrimSpace(value) != "", nil
}

// PublishManualStepContext writes a manual context override for stepName —
// the entry point for the MCP update_step_context tool
// (handleUpdateActiveStepContext in the daemon calls this). It owns the
// running-vs-paused row-status routing described in ROW-STATUS ROUTING above:
// pausedTmux (obtained from ResolveActiveStep) selects between the
// RUNNING-row writer and the COMPLETED-row writer. Returns the number of
// task_steps rows affected — 0 means the resolved step has no row in the
// expected status, which the caller should report as "not writable".
func (e *Engine) PublishManualStepContext(taskID int64, stepName, value string, appendMode, pausedTmux bool) (int64, error) {
	if pausedTmux {
		return e.database.UpdatePausedTmuxStepContext(taskID, stepName, value, appendMode)
	}
	return e.database.UpdateRunningTaskStepContext(taskID, stepName, value, appendMode)
}

// ResolveActiveStep returns the name of the step task t is currently "in" for
// step-context purposes, and whether that step is a tmux/human step paused at
// its approval gate. For a running agent step the active step is
// t.CurrentStep (row status 'running'). For a tmux/human step the engine has
// already marked the step's row 'completed' and cleared CurrentStep the
// instant it spawned the session — so the task could pause — even though the
// agent inside that session is still live and may want to fold its chat into
// the step context; in that case the active step is the paused step
// (PausedStep) and pausedTmux is true. Returns ("", false) when no step can
// be resolved (e.g. the task is idle).
func (e *Engine) ResolveActiveStep(t *task.Task) (stepName string, pausedTmux bool) {
	if t.CurrentStep != "" {
		return t.CurrentStep, false
	}
	if t.Status != task.StatusTmux || t.Workflow == "" {
		return "", false
	}
	wf := e.cfg.GetWorkflow(t.Workflow)
	step, ok := PausedStep(t, wf)
	if !ok {
		return "", false
	}
	return step.Name, true
}

// stepContextSource identifies which of the three precedence tiers (see the
// STEP-CONTEXT LIFECYCLE doc comment above) produced a step's context value.
type stepContextSource int

const (
	stepContextSourceNone stepContextSource = iota
	stepContextSourceManual
	stepContextSourceLastMessage
	stepContextSourceSummarizeChat
)

// decideInitialStepContext is the pure "manual > last_message > none"
// precedence decision for the value CompleteTaskStep writes the instant a
// step finishes — before any summarize_chat pass runs (which may overwrite it
// later; see decideSummarizeChat). Split out from captureHeadlessStepContext
// so the precedence chain can be unit-tested without touching the DB or
// spawning Claude.
func decideInitialStepContext(hasManualContext bool, manualContext string, strategy, resultText string) (source stepContextSource, value string, hasValue bool) {
	if hasManualContext {
		return stepContextSourceManual, manualContext, true
	}
	if strategy != config.SummarizationStrategyNone && resultText != "" {
		return stepContextSourceLastMessage, resultText, true
	}
	return stepContextSourceNone, "", false
}

// decideSummarizeChat is the pure gate for whether captureHeadlessStepContext
// should attempt a summarize_chat pass at all: never once a manual override
// has already won, and only when the step's strategy calls for it. A true
// result does not guarantee summarize_chat actually runs — the caller still
// needs a non-trivial chat log (see shouldSummarizeChat in summarizer.go).
func decideSummarizeChat(hasManualContext bool, strategy string) bool {
	return !hasManualContext && strategy == config.SummarizationStrategySummarizeChat
}

// captureHeadlessStepContext applies the manual > last_message > summarize_chat
// precedence (see the STEP-CONTEXT LIFECYCLE doc comment above) for a step
// that just finished running inside RunTask. resultText/exitCode/useTmux are
// the step's just-completed outcome. This covers both headless steps
// (resultText is the agent's result text) and the tmux fire-and-forget
// spawn path (resultText is always "" there, and the chat rarely exists yet
// at this point in time — this is a best-effort attempt for tmux;
// summarizePreviousTmuxStep in summarizer.go is the real capture point for
// tmux steps, see its doc comment).
func (e *Engine) captureHeadlessStepContext(ctx context.Context, t *task.Task, wf *config.WorkflowConfig, step config.StepConfig, resultText string, exitCode int, useTmux bool) {
	manualContext, hasManualContext, mErr := e.readManualOverride(t.ID, step.Name, false)
	if mErr != nil {
		log.Printf("Warning: failed to read running step context for step %q of task #%d: %v", step.Name, t.ID, mErr)
	}

	strategy := step.EffectiveSummarizationStrategy()

	// Record step completion with context. For summarize_chat, store
	// last_message immediately and (below) kick off synchronous summarization
	// that will overwrite the context when done. For "none", skip context
	// capture entirely so later steps see no context. A manual override is
	// preserved verbatim regardless of strategy.
	_, initialValue, hasInitialValue := decideInitialStepContext(hasManualContext, manualContext, strategy, resultText)
	var ctxPtr *string
	if hasInitialValue {
		ctxPtr = &initialValue
	}
	if err := e.database.CompleteTaskStep(t.ID, step.Name, ctxPtr, exitCode); err != nil {
		log.Printf("Warning: failed to complete task step record: %v", err)
	}

	if hasManualContext {
		log.Printf("Step %q of task #%d has a manual context override (%d chars); skipping summarize_chat", step.Name, t.ID, len(manualContext))
		return
	}

	if !decideSummarizeChat(hasManualContext, strategy) {
		return
	}

	chat, chatErr := e.loadStepChatContent(ctx, t, wf, step, useTmux)
	if chatErr != nil {
		log.Printf("Warning: failed to load chat content for step %q of task #%d: %v", step.Name, t.ID, chatErr)
		return
	}
	if chat == "" || !shouldSummarizeChat(chat, resultText, useTmux) {
		return
	}

	// Surface the step summarization phase via the task status so
	// the TUI can distinguish it from regular step execution.
	restore := e.markSummarizingStep(t, wf)
	summary, sumErr := e.summarizeChatLog(ctx, t, step.Name, step.SummarizationPrompt, chat)
	restore()
	if sumErr != nil {
		log.Printf("Warning: summarize_chat failed for step %q of task #%d: %v", step.Name, t.ID, sumErr)
		return
	}
	if summary == "" {
		return
	}
	if dbErr := e.database.UpdateTaskStepContext(t.ID, step.Name, summary); dbErr != nil {
		log.Printf("Warning: failed to update step context after summarize_chat for step %q of task #%d: %v", step.Name, t.ID, dbErr)
		return
	}
	log.Printf("summarize_chat updated step context for step %q of task #%d (%d chars)", step.Name, t.ID, len(summary))
}

// RecordTmuxStepSentinelSession records the chat session id for a tmux step
// from its turn-end sentinel payload (the optional `session_id` field — see
// the sentinel convention in sentinel.go). The sentinel is written from
// inside the very session that ran the step, so its session id is
// authoritative. This is the ONLY session-discovery mechanism: it gates which
// session the agent's chat_log_command reads for summarize_chat capture and
// which session resume_command restores after a daemon restart. No-op when
// there is no sentinel, it carries no session id, or the recorded session
// already matches.
func (e *Engine) RecordTmuxStepSentinelSession(t *task.Task, stepName string) {
	sentinel, ok := LatestStepSentinel(t.WorktreePath, stepName)
	if !ok || sentinel.SessionID == "" {
		return
	}
	existing, err := e.database.GetChatByStep(t.ID, stepName)
	if err == nil && existing != nil && existing.SessionID == sentinel.SessionID {
		return // already correct
	}
	if err := e.database.SetChatSessionID(t.ID, stepName, sentinel.SessionID); err != nil {
		log.Printf("Warning: failed to record sentinel session for task #%d step %q: %v", t.ID, stepName, err)
		return
	}
	if existing != nil && existing.SessionID != "" && existing.SessionID != sentinel.SessionID {
		log.Printf("Task #%d step %q: corrected chat session %q -> %q from turn-end sentinel", t.ID, stepName, existing.SessionID, sentinel.SessionID)
	}
}
