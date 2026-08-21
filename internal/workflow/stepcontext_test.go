package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/task"
)

// TestDecideInitialStepContext_Precedence is a table-driven test for the pure
// "manual > last_message > none" precedence decision made the instant a
// headless step finishes (see the STEP-CONTEXT LIFECYCLE doc comment in
// stepcontext.go). This is precedence tiers 1-2 of the chain; tier 3
// (summarize_chat) is gated separately by decideSummarizeChat below.
func TestDecideInitialStepContext_Precedence(t *testing.T) {
	tests := []struct {
		name             string
		hasManualContext bool
		manualContext    string
		strategy         string
		resultText       string
		wantSource       stepContextSource
		wantValue        string
		wantHasValue     bool
	}{
		{
			name:             "manual set wins over last_message",
			hasManualContext: true,
			manualContext:    "manually folded artifact",
			strategy:         config.SummarizationStrategySummarizeChat,
			resultText:       "claude result text",
			wantSource:       stepContextSourceManual,
			wantValue:        "manually folded artifact",
			wantHasValue:     true,
		},
		{
			name:             "manual set wins even over strategy none",
			hasManualContext: true,
			manualContext:    "manually folded artifact",
			strategy:         config.SummarizationStrategyNone,
			resultText:       "",
			wantSource:       stepContextSourceManual,
			wantValue:        "manually folded artifact",
			wantHasValue:     true,
		},
		{
			name:             "no manual, non-empty resultText is captured as last_message",
			hasManualContext: false,
			strategy:         config.SummarizationStrategySummarizeChat,
			resultText:       "claude result text",
			wantSource:       stepContextSourceLastMessage,
			wantValue:        "claude result text",
			wantHasValue:     true,
		},
		{
			name:             "no manual, no output, summarize_chat configured: no initial value",
			hasManualContext: false,
			strategy:         config.SummarizationStrategySummarizeChat,
			resultText:       "",
			wantSource:       stepContextSourceNone,
			wantValue:        "",
			wantHasValue:     false,
		},
		{
			name:             "no manual, resultText present but strategy is none: no initial value",
			hasManualContext: false,
			strategy:         config.SummarizationStrategyNone,
			resultText:       "claude result text",
			wantSource:       stepContextSourceNone,
			wantValue:        "",
			wantHasValue:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source, value, hasValue := decideInitialStepContext(tt.hasManualContext, tt.manualContext, tt.strategy, tt.resultText)
			if source != tt.wantSource {
				t.Errorf("source = %v, want %v", source, tt.wantSource)
			}
			if value != tt.wantValue {
				t.Errorf("value = %q, want %q", value, tt.wantValue)
			}
			if hasValue != tt.wantHasValue {
				t.Errorf("hasValue = %v, want %v", hasValue, tt.wantHasValue)
			}
		})
	}
}

// TestDecideSummarizeChat_Precedence covers precedence tier 3: whether a
// summarize_chat pass should even be attempted. A manual override (tier 1)
// always blocks it, regardless of strategy.
func TestDecideSummarizeChat_Precedence(t *testing.T) {
	tests := []struct {
		name             string
		hasManualContext bool
		strategy         string
		want             bool
	}{
		{"manual override blocks summarize_chat", true, config.SummarizationStrategySummarizeChat, false},
		{"no manual + summarize_chat strategy: attempt it", false, config.SummarizationStrategySummarizeChat, true},
		{"no manual + last_message strategy: do not attempt", false, config.SummarizationStrategyLastMessage, false},
		{"no manual + none strategy: do not attempt", false, config.SummarizationStrategyNone, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decideSummarizeChat(tt.hasManualContext, tt.strategy); got != tt.want {
				t.Errorf("decideSummarizeChat(%v, %q) = %v, want %v", tt.hasManualContext, tt.strategy, got, tt.want)
			}
		})
	}
}

// TestReadManualOverride_RowStatusRouting proves readManualOverride reads the
// row-status-correct source: the RUNNING row when pausedTmux is false (a
// headless step still executing), and the COMPLETED row when pausedTmux is
// true (a tmux/human step already paused at its approval gate). This is the
// read-side half of the ROW-STATUS ROUTING invariant in stepcontext.go.
func TestReadManualOverride_RowStatusRouting(t *testing.T) {
	t.Run("pausedTmux=false reads the running row, not the completed row", func(t *testing.T) {
		store := newFakeTaskStore()
		store.runningStepContexts[1] = map[string]string{"implementing": "manual mid-step write"}
		store.stepContexts[1] = map[string]string{"implementing": "stale completed-row value"}
		e := &Engine{database: store}

		value, has, err := e.readManualOverride(1, "implementing", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has || value != "manual mid-step write" {
			t.Errorf("got (%q, %v), want (%q, true)", value, has, "manual mid-step write")
		}
	})

	t.Run("pausedTmux=true reads the completed row, not the running row", func(t *testing.T) {
		store := newFakeTaskStore()
		store.runningStepContexts[1] = map[string]string{"grilling": "stale running-row value"}
		store.stepContexts[1] = map[string]string{"grilling": "manually folded chat"}
		e := &Engine{database: store}

		value, has, err := e.readManualOverride(1, "grilling", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has || value != "manually folded chat" {
			t.Errorf("got (%q, %v), want (%q, true)", value, has, "manually folded chat")
		}
	})

	t.Run("blank value reports has=false", func(t *testing.T) {
		store := newFakeTaskStore()
		e := &Engine{database: store}

		if _, has, err := e.readManualOverride(1, "implementing", false); err != nil || has {
			t.Errorf("got has=%v err=%v, want has=false err=nil for an unset row", has, err)
		}
	})
}

// TestPublishManualStepContext_RowStatusRouting proves PublishManualStepContext
// routes a write to exactly one of the two row-status-specific DB writers,
// selected by pausedTmux — the write-side half of the ROW-STATUS ROUTING
// invariant, and the sole place callers (the daemon's
// handleUpdateActiveStepContext) need to consult.
func TestPublishManualStepContext_RowStatusRouting(t *testing.T) {
	t.Run("pausedTmux=false calls the running-row writer only", func(t *testing.T) {
		store := newFakeTaskStore()
		e := &Engine{database: store}

		rows, err := e.PublishManualStepContext(1, "implementing", "canonical artifact", false, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rows != 1 {
			t.Errorf("rows = %d, want 1", rows)
		}
		if len(store.updateRunningCalls) != 1 || len(store.updatePausedCalls) != 0 {
			t.Errorf("expected exactly one running-row write and zero paused-row writes, got running=%d paused=%d",
				len(store.updateRunningCalls), len(store.updatePausedCalls))
		}
	})

	t.Run("pausedTmux=true calls the paused-row writer only", func(t *testing.T) {
		store := newFakeTaskStore()
		e := &Engine{database: store}

		rows, err := e.PublishManualStepContext(1, "grilling", "folded chat", false, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rows != 1 {
			t.Errorf("rows = %d, want 1", rows)
		}
		if len(store.updatePausedCalls) != 1 || len(store.updateRunningCalls) != 0 {
			t.Errorf("expected exactly one paused-row write and zero running-row writes, got running=%d paused=%d",
				len(store.updateRunningCalls), len(store.updatePausedCalls))
		}
	})
}

// TestResolveActiveStep covers the three cases ResolveActiveStep must
// distinguish: a running agent step, a tmux/human step paused at its
// approval gate, and a task with no resolvable active step.
func TestResolveActiveStep(t *testing.T) {
	wf := config.WorkflowConfig{
		Name: "wf",
		Steps: []config.StepConfig{
			{Name: "planning"},
			{Name: "grilling", Human: true},
		},
	}
	cfg := &config.Config{Workflows: []config.WorkflowConfig{wf}}
	e := &Engine{database: newFakeTaskStore(), cfg: newEngineConfig(cfg)}

	t.Run("running agent step: CurrentStep wins, not paused", func(t *testing.T) {
		tk := &task.Task{Workflow: "wf", CurrentStep: "planning", Status: task.StatusRunning}
		name, pausedTmux := e.ResolveActiveStep(tk)
		if name != "planning" || pausedTmux {
			t.Errorf("got (%q, %v), want (%q, false)", name, pausedTmux, "planning")
		}
	})

	t.Run("tmux step paused at approval gate: PausedStep wins, pausedTmux=true", func(t *testing.T) {
		// CurrentStep is cleared and StepIndex bumped past the paused step, per
		// the cursor invariant in cursor.go (index 2 resolves PausedStep to
		// Steps[1] == "grilling").
		tk := &task.Task{Workflow: "wf", CurrentStep: "", StepIndex: 2, Status: task.StatusTmux}
		name, pausedTmux := e.ResolveActiveStep(tk)
		if name != "grilling" || !pausedTmux {
			t.Errorf("got (%q, %v), want (%q, true)", name, pausedTmux, "grilling")
		}
	})

	t.Run("idle task: no active step", func(t *testing.T) {
		tk := &task.Task{Workflow: "wf", CurrentStep: "", Status: task.StatusPending}
		name, pausedTmux := e.ResolveActiveStep(tk)
		if name != "" || pausedTmux {
			t.Errorf("got (%q, %v), want (\"\", false)", name, pausedTmux)
		}
	})
}

// TestRecordTmuxStepSentinelSession_CorrectsFromSentinel proves the sentinel
// path (audit item: tmux_monitor's captureSentinelSession) writes through the
// Engine's taskStore rather than a daemon-level DB call, and that it corrects
// a stale recorded session id.
func TestRecordTmuxStepSentinelSession_CorrectsFromSentinel(t *testing.T) {
	worktree := t.TempDir()
	writeSentinel(t, worktree, "grilling-1234567890.json", `{"session_id":"new-session"}`)

	store := newFakeTaskStore()
	e := &Engine{database: store}
	tk := &task.Task{ID: 1, WorktreePath: worktree}

	// No prior recorded session.
	e.RecordTmuxStepSentinelSession(tk, "grilling")
	got, err := store.GetChatByStep(1, "grilling")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.SessionID != "new-session" {
		t.Fatalf("got %+v, want session id %q", got, "new-session")
	}

	// A second call with the same sentinel content must not error or drop the
	// recorded session (it's a no-op: already correct).
	e.RecordTmuxStepSentinelSession(tk, "grilling")
	got2, _ := store.GetChatByStep(1, "grilling")
	if got2.SessionID != "new-session" {
		t.Errorf("session id changed on idempotent re-call: %q", got2.SessionID)
	}
}

// TestRecordTmuxStepSentinelSession_NoSessionIDIsNoOp verifies that a sentinel
// whose payload carries no session_id (an empty object, or only a
// transcript_path) records nothing: the sentinel's existence is a turn-end
// signal, but session discovery requires the session_id field.
func TestRecordTmuxStepSentinelSession_NoSessionIDIsNoOp(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"empty object", `{}`},
		{"transcript_path without session_id", `{"transcript_path":"/x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worktree := t.TempDir()
			writeSentinel(t, worktree, "grilling-1234567890.json", tt.payload)

			store := newFakeTaskStore()
			e := &Engine{database: store}
			tk := &task.Task{ID: 1, WorktreePath: worktree}

			e.RecordTmuxStepSentinelSession(tk, "grilling")

			got, err := store.GetChatByStep(1, "grilling")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != nil {
				t.Errorf("expected no chat row for a session-id-less sentinel, got %+v", got)
			}
		})
	}
}

// TestRecordTmuxStepSentinelSession_CorrectsStaleRecordedSession verifies the
// correction path: when a session was already recorded but the authoritative
// sentinel (written from inside the very session that ran the step) carries a
// different session_id, the recorded row is updated to the sentinel's value.
func TestRecordTmuxStepSentinelSession_CorrectsStaleRecordedSession(t *testing.T) {
	worktree := t.TempDir()
	writeSentinel(t, worktree, "grilling-1234567890.json", `{"session_id":"new-id"}`)

	store := newFakeTaskStore()
	if err := store.SetChatSessionID(1, "grilling", "old-id"); err != nil {
		t.Fatalf("SetChatSessionID: %v", err)
	}
	e := &Engine{database: store}
	tk := &task.Task{ID: 1, WorktreePath: worktree}

	e.RecordTmuxStepSentinelSession(tk, "grilling")

	got, err := store.GetChatByStep(1, "grilling")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.SessionID != "new-id" {
		t.Errorf("got %+v, want session id corrected to %q", got, "new-id")
	}
}

// TestCaptureHeadlessStepContextNoSummarizerDegrades pins the ErrNoSummarizer
// degradation through captureHeadlessStepContext: a headless step with the
// summarize_chat strategy and a non-trivial chat log, but NO `summarizer:`
// command configured, keeps its already-captured last_message context (the
// failed summarize pass must not clobber it) and does not fail the task —
// even with require_context set, which only gates the tmux capture path
// (summarizePreviousTmuxStep), not this one.
func TestCaptureHeadlessStepContextNoSummarizerDegrades(t *testing.T) {
	wf := config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{{
			Name:                  "implement",
			Prompt:                "do the thing",
			SummarizationStrategy: config.SummarizationStrategySummarizeChat,
			RequireContext:        true,
		}},
	}
	engine, tk, runner, database := newFakeRunnerTestEngine(t, wf)

	// Pre-write a unified task log with a step region larger than
	// smallChatBytes so shouldSummarizeChat is true and the summarize pass is
	// actually attempted (and hits ErrNoSummarizer — the helper's config has
	// no summarizer command).
	if err := os.MkdirAll(ProjectLogsDir(engine.dataDir, tk.ID), 0755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	logContent := fmt.Sprintf("[10:00:00] === Step: implement (task #%d) ===\n", tk.ID) +
		strings.Repeat("[10:00:01] chat line with meaningful implementation detail\n", smallChatBytes/16)
	if err := os.WriteFile(ProjectLogPath(engine.dataDir, tk.ID), []byte(logContent), 0644); err != nil {
		t.Fatalf("write task log: %v", err)
	}

	runner.script("implement", fakeAgentResult{exitCode: 0, resultText: "last-message context"})

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask must not fail when the summarizer is unconfigured (best-effort degradation), got %v", err)
	}

	got, err := database.GetTaskStepContext(tk.ID, "implement")
	if err != nil {
		t.Fatalf("GetTaskStepContext: %v", err)
	}
	if got != "last-message context" {
		t.Errorf("step context = %q, want the last_message value %q left intact", got, "last-message context")
	}

	refreshed, err := database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if refreshed.Status != task.StatusRunning {
		t.Errorf("Status = %q, want unchanged %q (degradation must not fail the task)", refreshed.Status, task.StatusRunning)
	}
	if refreshed.ExitCode != nil {
		t.Errorf("ExitCode = %v, want nil (no failure recorded)", *refreshed.ExitCode)
	}
}

// TestCaptureHeadlessStepContextSummarizeChatOverwrites pins the success half
// of the summarize_chat strategy (the failure half is the NoSummarizer test
// above): with a configured summarizer command, the summary it produces
// REPLACES the initially-captured last_message context via
// UpdateTaskStepContext, and the summarizer is invoked with
// SAKUSEN_PURPOSE=summarize_chat.
func TestCaptureHeadlessStepContextSummarizeChatOverwrites(t *testing.T) {
	wf := config.WorkflowConfig{
		Name: "default",
		Steps: []config.StepConfig{{
			Name:                  "implement",
			Prompt:                "do the thing",
			SummarizationStrategy: config.SummarizationStrategySummarizeChat,
		}},
	}
	engine, tk, runner, database := newFakeRunnerTestEngine(t, wf)

	// Configure a summarizer post-construction via the engineConfig snapshot
	// (same-package seam; the helper's config has none). The stub records its
	// SAKUSEN_PURPOSE and emits a fixed summary.
	purposeLog := filepath.Join(t.TempDir(), "purpose.log")
	engine.cfg.Summarizer = config.SummarizerConfig{
		Command: fmt.Sprintf(`cat > /dev/null; echo "$SAKUSEN_PURPOSE" >> %q; echo chat-summary`, purposeLog),
	}

	// A unified task log with a step region larger than smallChatBytes so
	// shouldSummarizeChat fires.
	if err := os.MkdirAll(ProjectLogsDir(engine.dataDir, tk.ID), 0755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	logContent := fmt.Sprintf("[10:00:00] === Step: implement (task #%d) ===\n", tk.ID) +
		strings.Repeat("[10:00:01] chat line with meaningful implementation detail\n", smallChatBytes/16)
	if err := os.WriteFile(ProjectLogPath(engine.dataDir, tk.ID), []byte(logContent), 0644); err != nil {
		t.Fatalf("write task log: %v", err)
	}

	runner.script("implement", fakeAgentResult{exitCode: 0, resultText: "last-message context"})

	if err := engine.RunTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	got, err := database.GetTaskStepContext(tk.ID, "implement")
	if err != nil {
		t.Fatalf("GetTaskStepContext: %v", err)
	}
	if got != "chat-summary" {
		t.Errorf("step context = %q, want the summarize_chat output %q to overwrite the last_message value", got, "chat-summary")
	}

	purposes, err := os.ReadFile(purposeLog)
	if err != nil {
		t.Fatalf("summarizer stub never ran: %v", err)
	}
	if strings.TrimSpace(string(purposes)) != "summarize_chat" {
		t.Errorf("SAKUSEN_PURPOSE log = %q, want a single %q invocation", string(purposes), "summarize_chat")
	}
}
