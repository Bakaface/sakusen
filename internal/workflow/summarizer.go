package workflow

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/runner"
	"github.com/Bakaface/sakusen/internal/task"
)

// CheckFastTrackCompletion is the single owner of the "no meaningful changes"
// fast-track rule: whether a task whose workflow run has finished can skip
// the full finalization pipeline (FinalizeTask: merge → summarize → cleanup)
// and instead be completed directly. It previously existed as two
// near-verbatim copies — daemon/broadcast.go's finalizeCompletedTask (the
// agent-completion path) and daemon/handlers_continue.go's advanceTmuxTask
// (the tmux-advance / Finalize-request path) — that both computed
// HasMeaningfulChanges against the same noiseFiles list and diverged only in
// what they did AFTER the decision (notifications, response messages). That
// divergent tail stays daemon-side; see the daemon's maybeFastTrackCompletion
// helper (broadcast.go) which both call sites now share for the identical
// side effects (cleanup, status, broadcast) and layer their own extra
// behavior on top of.
//
// noiseFiles enumerates paths that don't count toward "meaningful" (e.g.
// .sakusen-output.log). Ownership of that list stays with the
// daemon (it's daemon/tmux bookkeeping, not a workflow config concept) —
// it's passed in rather than hardcoded here.
//
// Returns false (do full finalization) when t has no worktree path or is a
// non-worktree task, or when the meaningful-changes check itself errors —
// callers should log the error and fall through to full finalization rather
// than silently completing a task that might have real, uninspected work.
func (e *Engine) CheckFastTrackCompletion(t *task.Task, noiseFiles []string) (fastTrack bool, err error) {
	if t.WorktreePath == "" || !t.Worktree {
		return false, nil
	}
	hasChanges, err := e.repo.HasMeaningfulChanges(t.WorktreePath, noiseFiles)
	if err != nil {
		return false, err
	}
	return !hasChanges, nil
}

// FinalizeTask runs the on_complete action, then the summarizer, then worktree cleanup.
// Used when finalizing a tmux-continued task.
func (e *Engine) FinalizeTask(ctx context.Context, t *task.Task) error {
	// Append finalize progress to the unified task log so the TUI's log view
	// shows it in chronological order alongside step output.
	logDir := ProjectLogsDir(e.dataDir, t.ID)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Printf("Warning: failed to create log dir for task #%d: %v", t.ID, err)
	}
	logPath := ProjectLogPath(e.dataDir, t.ID)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("Warning: failed to open task log for task #%d: %v", t.ID, err)
	}
	defer func() {
		if logFile != nil {
			logFile.Close()
		}
	}()

	logFn := func(format string, args ...any) {
		msg := fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
		log.Printf("Task #%d finalize: %s", t.ID, fmt.Sprintf(format, args...))
		if logFile != nil {
			logFile.WriteString(msg + "\n")
		}
	}

	logFn("=== Finalization started for task #%d: %s ===", t.ID, t.Title)

	// Resolve branch name if not set (may not have been persisted to DB)
	if t.Worktree && t.Branch == "" {
		t.Branch = e.cfg.ResolveBranchForTask(t.ID, t.Title, t.Slug, t.BranchName)
	}

	// If the workflow's last step was a tmux step with summarize_chat, capture
	// its chat summary now — RunTask cannot do this synchronously (the chat is
	// still being written when the step pauses) and advanceTmuxTask bypasses
	// ResumeAfterApproval when there are no more steps, so this is the only
	// remaining hook. When the step is marked require_context and capture
	// fails, block finalization (before on_complete/merge) so the task fails
	// loudly instead of merging with an empty step context.
	if err := e.summarizePreviousTmuxStep(ctx, t, logFn); err != nil {
		logFn("Error: blocking finalization — %v", err)
		return err
	}

	// Capture the diff stat BEFORE on_complete runs. After the task branch
	// is merged into main, it's fully reachable from main, which makes
	// post-merge DiffStat return empty — the summarizer would then see no
	// changes and abort. Computing it here preserves the fallback signal.
	var preMergeDiffStat string
	if t.Worktree && t.WorktreePath != "" {
		baseBranch := e.effectiveBaseBranch(t)
		var diffErr error
		preMergeDiffStat, diffErr = e.repo.DiffStat(t.WorktreePath, baseBranch)
		if diffErr != nil {
			logFn("Warning: failed to compute pre-merge diff stat for task #%d: %v", t.ID, diffErr)
		}
	}

	// Run on_complete action first (merge to unblock user)
	action := e.effectiveOnComplete(t)
	if action == "" {
		action = "none"
	}
	logFn("Running on_complete action: %s", action)
	if err := e.executeOnComplete(ctx, t, nil, logFn); err != nil {
		logFn("Error: on_complete failed: %v", err)
		return err
	}

	// Run summarizer after merge. For single-step workflows the per-step
	// summary already IS the task summary — promote it directly into
	// task.context and skip the redundant cross-step Claude invocation.
	wf := e.cfg.GetWorkflow(t.Workflow)
	if wf != nil {
		if !e.promoteSingleStepContextToTask(t, wf, logFn) {
			if err := e.database.UpdateTaskStatus(t.ID, task.StatusSummarizing); err != nil {
				logFn("Warning: failed to set summarizing status: %v", err)
			}
			logFn("Running summarizer...")
			if err := e.runSummarizer(ctx, t, wf, preMergeDiffStat, logFn); err != nil {
				logFn("Warning: summarizer failed: %v", err)
			} else {
				logFn("Summarizer completed")
			}
		}
	}

	// Clean up worktree after summarizer (if merge was performed)
	if e.effectiveOnComplete(t) == "merge" && t.Worktree {
		e.cleanupMergedWorktree(t, logFn)
	}

	logFn("=== Finalization completed ===")
	return nil
}

// promoteSingleStepContextToTask copies the single step's already-captured
// summary into the task's context, bypassing the cross-step task summarizer.
// Returns true when the promotion happened and the caller should skip
// runSummarizer; false when the workflow has more than one step or the single
// step has no usable context (in which case the caller should fall through to
// runSummarizer so its git-diff fallback can still produce a task summary).
func (e *Engine) promoteSingleStepContextToTask(t *task.Task, wf *config.WorkflowConfig, logFn func(string, ...any)) bool {
	if wf == nil || len(wf.Steps) != 1 {
		return false
	}
	stepName := wf.Steps[0].Name
	stepCtx, err := e.database.GetTaskStepContext(t.ID, stepName)
	if err != nil {
		if logFn != nil {
			logFn("Warning: failed to read step %q context for promotion: %v", stepName, err)
		}
		return false
	}
	stepCtx = strings.TrimSpace(stepCtx)
	if stepCtx == "" {
		return false
	}
	if err := e.database.UpdateTaskContext(t.ID, stepCtx); err != nil {
		if logFn != nil {
			logFn("Warning: failed to promote step %q context to task #%d: %v", stepName, t.ID, err)
		}
		return false
	}
	t.Context = stepCtx
	if logFn != nil {
		logFn("Promoted step %q context to task #%d context (%d chars); skipping cross-step summarizer", stepName, t.ID, len(stepCtx))
	}
	return true
}

// runSummarizer generates a summary of all artifacts and stores it as the task's context.
// preMergeDiffStat is the `git diff --stat` output captured BEFORE on_complete ran;
// the post-merge worktree has no diff against the base branch, so the caller must
// pass the pre-merge value (empty when unavailable).
// logFn is optional; when provided, progress messages are written to it (e.g. finalize log).
func (e *Engine) runSummarizer(ctx context.Context, t *task.Task, wf *config.WorkflowConfig, preMergeDiffStat string, logFn func(string, ...any)) error {
	logMsg := func(format string, args ...any) {
		log.Printf(format, args...)
		if logFn != nil {
			logFn(format, args...)
		}
	}
	// Collect step names
	var stepNames []string
	for _, s := range wf.Steps {
		stepNames = append(stepNames, s.Name)
	}

	// Collect step contexts from DB
	stepContexts, err := e.database.GetTaskStepContexts(t.ID, stepNames)
	if err != nil {
		logMsg("Warning: failed to get step contexts for task #%d: %v", t.ID, err)
		stepContexts = make(map[string]string)
	}

	// Use pre-merge diff stat as fallback context when no step contexts are available
	diffStat := strings.TrimSpace(preMergeDiffStat)
	if len(stepContexts) == 0 && diffStat == "" {
		logMsg("No step contexts or changes found for task #%d, skipping summarizer", t.ID)
		return nil
	}

	// Build the prompt and log the summarization approach
	var prompt string
	if wf.SummarizerPrompt != "" {
		// Use the configured summarizer prompt with template resolution
		tmplCtx := e.buildTemplateContext(t, TaskVars{
			ID:      t.ID,
			Title:   t.Title,
			Input:   ResolveTaskRefs(t.Input, e.database.GetTask),
			Context: ResolveTaskRefs(t.Context, e.database.GetTask),
			Slug:    t.Slug,
			Branch:  t.Branch,
		}, stepContexts, LoopVars{})
		prompt = ResolveTemplate(wf.SummarizerPrompt, tmplCtx)
		var names []string
		for name := range stepContexts {
			names = append(names, name)
		}
		logMsg("%s", summarizationDescription(t.ID, true, names, false))
	} else if len(stepContexts) > 0 {
		// Build default prompt with all step contexts
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Summarize the progress made on task #%d: %s\n\n", t.ID, t.Title))
		sb.WriteString("Use the context from the following task step contexts:\n\n")
		var contextNames []string
		for _, name := range stepNames {
			if content, ok := stepContexts[name]; ok {
				sb.WriteString(fmt.Sprintf("### %s\n\n%s\n\n", name, content))
				contextNames = append(contextNames, name)
			}
		}
		sb.WriteString("Provide a concise but comprehensive summary of what was accomplished, ")
		sb.WriteString("any decisions made, and the current state of the implementation. ")
		sb.WriteString("This summary will be used as context for future work on this task.")
		prompt = sb.String()
		logMsg("%s", summarizationDescription(t.ID, false, contextNames, false))
	} else {
		// No artifacts — use git diff stat and instruct Claude to read the actual changes
		prompt = BuildDiffStatSummaryPrompt(t.ID, t.Title, t.Input, diffStat)
		logMsg("%s", summarizationDescription(t.ID, false, nil, true))
	}

	logMsg("Running summarizer for task #%d", t.ID)

	// Run the configured summarizer command synchronously to capture the
	// summary text.
	summary, err := e.runSummarizerSync(ctx, prompt, t.WorktreePath, "summarize")
	if err != nil {
		return fmt.Errorf("summarizer invocation failed: %w", err)
	}

	summary = strings.TrimSpace(summary)
	if summary == "" {
		logMsg("Summarizer produced empty output for task #%d", t.ID)
		return nil
	}

	// Store the context in the database
	if err := e.database.UpdateTaskContext(t.ID, summary); err != nil {
		return fmt.Errorf("failed to store task context: %w", err)
	}

	t.Context = summary
	logMsg("Summarizer completed for task #%d (%d chars)", t.ID, len(summary))
	return nil
}

// BuildDiffStatSummaryPrompt constructs the diff-stat-fallback summarizer
// prompt: the prompt used when no step contexts are available and only the
// list of changed files is known. Exported so the backfill CLI can reuse the
// exact prompt shape used by live finalization.
func BuildDiffStatSummaryPrompt(taskID int64, title, input, diffStat string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Summarize the progress made on task #%d: %s\n\n", taskID, title))
	sb.WriteString("The task input was:\n")
	sb.WriteString(input)
	sb.WriteString("\n\nThe following files were changed:\n\n```\n")
	sb.WriteString(diffStat)
	sb.WriteString("\n```\n\n")
	sb.WriteString("Read the changed files listed above and review the actual code to understand what was implemented. ")
	sb.WriteString("Do NOT guess or assume — base your summary on the actual file contents and git changes in this repository. ")
	sb.WriteString("Provide a concise summary of what was accomplished. ")
	sb.WriteString("This summary will be used as context for future work on this task.")
	return sb.String()
}

// loadStepChatContent returns the raw chat content for a step.
// For tmux steps, runs the step agent's chat_log_command (which prints the
// conversation log on stdout, typically located via the recorded session id or
// the latest sentinel's transcript_path). For headless steps, reads the
// per-step region of the unified task log.
// Returns empty string (no error) if no content is available yet.
func (e *Engine) loadStepChatContent(ctx context.Context, t *task.Task, wf *config.WorkflowConfig, step config.StepConfig, useTmux bool) (string, error) {
	if useTmux {
		_, agent, agentErr := e.cfg.StepAgent(wf, &step)
		if agentErr != nil {
			return "", fmt.Errorf("failed to load chat for tmux step %q: %w", step.Name, agentErr)
		}
		if strings.TrimSpace(agent.ChatLogCommand) == "" {
			// The agent record has no chat-log mechanism — nothing to feed the
			// summarizer. Callers treat "" as "no chat content" (warn, or fail
			// when require_context is set).
			log.Printf("Step %q of task #%d: agent has no chat_log_command; no chat content available for summarize_chat", step.Name, t.ID)
			return "", nil
		}

		// Look up the session id recorded from the step's sentinel payload.
		env := map[string]string{
			"SAKUSEN_TASK_ID":  fmt.Sprintf("%d", t.ID),
			"SAKUSEN_STEP":     step.Name,
			"SAKUSEN_WORKTREE": t.WorktreePath,
		}
		if chat, err := e.database.GetChatByStep(t.ID, step.Name); err != nil {
			return "", fmt.Errorf("failed to look up chat session for tmux step %q: %w", step.Name, err)
		} else if chat != nil {
			env["SAKUSEN_SESSION_ID"] = chat.SessionID
		}
		if sentinel, sentinelPath, ok := LatestStepSentinelWithPath(t.WorktreePath, step.Name); ok {
			env["SAKUSEN_SENTINEL_FILE"] = sentinelPath
			if sentinel.TranscriptPath != "" {
				env["SAKUSEN_TRANSCRIPT_PATH"] = sentinel.TranscriptPath
			}
		}

		cmdCtx, cancel := context.WithTimeout(ctx, chatLogCommandTimeout)
		defer cancel()
		out, err := runner.RunSync(cmdCtx, agent.ChatLogCommand, t.WorktreePath, runner.MergeEnv(env, agent.Env), "")
		if err != nil {
			return "", fmt.Errorf("chat_log_command failed for tmux step %q: %w", step.Name, err)
		}
		return strings.TrimSpace(out), nil
	}

	// Headless step: slice the most recent run of this step out of the unified
	// task log. The step header and footer (written by runHeadlessAgent) act as
	// region markers; retries leave multiple header/footer pairs in the file
	// and we want the most recent.
	logPath := ProjectLogPath(e.dataDir, t.ID)
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to read task log for step %q: %w", step.Name, err)
	}
	return extractLatestStepRegion(string(data), step.Name), nil
}

// chatLogCommandTimeout bounds a chat_log_command invocation — it should read
// a file or two, never run an interactive tool.
const chatLogCommandTimeout = 30 * time.Second

// extractLatestStepRegion returns the slice of the unified task log corresponding
// to the most recent run of the given step. Returns an empty string if no header
// for the step is present.
func extractLatestStepRegion(content, stepName string) string {
	headerNeedle := fmt.Sprintf("=== Step: %s (task #", stepName)
	footerNeedle := fmt.Sprintf("=== Step %s finished ", stepName)

	lines := strings.Split(content, "\n")
	lastHeader := -1
	lastFooter := -1
	for i, line := range lines {
		if strings.Contains(line, headerNeedle) {
			lastHeader = i
			lastFooter = -1
		} else if lastHeader >= 0 && strings.Contains(line, footerNeedle) {
			lastFooter = i
		}
	}
	if lastHeader < 0 {
		return ""
	}
	end := len(lines)
	if lastFooter > lastHeader {
		end = lastFooter + 1
	}
	return strings.TrimSpace(strings.Join(lines[lastHeader:end], "\n"))
}

// smallChatBytes is the threshold below which a non-tmux step skips the
// summarization pass and keeps the agent's result text as step context —
// a chat log that small is too short to be worth paying a summarizer round
// trip for.
const smallChatBytes = 4096

// shouldSummarizeChat returns true when the chat log is worth running a
// summarization pass over. For non-tmux steps with a non-empty result text and
// a tiny chat log, the result text is kept and the summarizer is skipped. Tmux
// steps always summarize because they have no result-text fallback.
func shouldSummarizeChat(chat, resultText string, useTmux bool) bool {
	if useTmux {
		return true
	}
	if strings.TrimSpace(resultText) == "" {
		return true
	}
	return len(chat) >= smallChatBytes
}

// summarizeChatLog summarises the given chat content via the configured
// summarizer command. When `summarizer.max_prompt_bytes` is set and the
// resolved prompt exceeds it, the chat is summarised map-reduce style: split
// on line boundaries into chunks below the ceiling, each chunk summarised with
// a generic extraction prompt, then the chunk summaries fed back through the
// original (customPrompt or default) final-summary prompt.
//
// customPrompt is a template that may reference the chat via a {{chat}}
// placeholder; task template variables ({{task.id}}, {{task.title}}, etc.) are
// also resolved. If customPrompt is empty, the default summarization prompt is
// used.
func (e *Engine) summarizeChatLog(ctx context.Context, t *task.Task, stepName, customPrompt, chatContent string) (string, error) {
	if strings.TrimSpace(chatContent) == "" {
		return "", nil
	}

	prompt := e.buildSummarizePrompt(t, stepName, customPrompt, chatContent)

	maxBytes := e.cfg.Summarizer.MaxPromptBytes
	if maxBytes > 0 && len(prompt) > maxBytes {
		log.Printf("summarize_chat: prompt %d bytes exceeds summarizer.max_prompt_bytes (%d) for step %q of task #%d; running map-reduce", len(prompt), maxBytes, stepName, t.ID)
		chunkSummaries, err := e.summarizeChatChunks(ctx, t, stepName, chatContent, maxBytes)
		if err != nil {
			return "", err
		}
		reduced := strings.Join(chunkSummaries, "\n\n--- CHUNK BOUNDARY ---\n\n")
		prompt = e.buildSummarizePrompt(t, stepName, customPrompt, reduced)
		log.Printf("summarize_chat: map-reduce reduce step for step %q of task #%d (%d chunk summaries, %d chars)", stepName, t.ID, len(chunkSummaries), len(reduced))
	}

	log.Printf("Running summarize_chat for step %q of task #%d (prompt %d bytes)", stepName, t.ID, len(prompt))
	summary, err := e.runSummarizerSync(ctx, prompt, t.WorktreePath, "summarize_chat")
	if err != nil {
		return "", fmt.Errorf("summarize_chat invocation failed: %w", err)
	}
	summary = strings.TrimSpace(summary)
	log.Printf("summarize_chat completed for step %q of task #%d (%d chars)", stepName, t.ID, len(summary))
	return summary, nil
}

// buildSummarizePrompt resolves the summarization prompt for the given chat content,
// using customPrompt (template, with optional {{chat}} placeholder) if non-empty,
// or a sensible default otherwise.
func (e *Engine) buildSummarizePrompt(t *task.Task, stepName, customPrompt, chatContent string) string {
	if customPrompt != "" {
		tmplCtx := e.buildTemplateContext(t, TaskVars{
			ID:      t.ID,
			Title:   t.Title,
			Input:   ResolveTaskRefs(t.Input, e.database.GetTask),
			Context: ResolveTaskRefs(t.Context, e.database.GetTask),
			Slug:    t.Slug,
			Branch:  t.Branch,
		}, nil, LoopVars{})
		resolved := ResolveTemplate(customPrompt, tmplCtx)

		if strings.Contains(resolved, "{{chat}}") {
			return strings.ReplaceAll(resolved, "{{chat}}", chatContent)
		}
		return resolved + "\n\n--- CONVERSATION LOG ---\n" + chatContent
	}

	return fmt.Sprintf(
		"Summarize the following agent conversation log from step %q of task #%d: %s\n\n"+
			"Output requirements:\n"+
			"- Under 200 words.\n"+
			"- Preserve file paths, function/symbol names, command lines, and error strings VERBATIM — do not paraphrase identifiers.\n"+
			"- Cover what was accomplished, key decisions, files changed, and any blockers or unresolved issues.\n"+
			"- Prioritise actionable detail over narrative; this summary becomes context for later workflow steps.\n\n"+
			"--- CONVERSATION LOG ---\n%s",
		stepName, t.ID, t.Title, chatContent,
	)
}

// chunkHeadroomBytes is subtracted from summarizer.max_prompt_bytes when
// sizing map-reduce chunks, leaving room for the surrounding instruction
// prompt.
const chunkHeadroomBytes = 30 * 1024

// summarizeChatChunks splits chatContent on line boundaries (sized below
// maxPromptBytes, minus instruction headroom) and runs an extraction pass over
// each chunk, returning the per-chunk summaries.
func (e *Engine) summarizeChatChunks(ctx context.Context, t *task.Task, stepName, chatContent string, maxPromptBytes int) ([]string, error) {
	chunkBytes := maxPromptBytes - chunkHeadroomBytes
	if chunkBytes < 1024 {
		chunkBytes = maxPromptBytes // tiny ceilings: skip the headroom math
	}
	chunks := splitOnLineBoundary(chatContent, chunkBytes)
	summaries := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		mapPrompt := fmt.Sprintf(
			"This is chunk %d of %d from an agent conversation log (step %q of task #%d: %s).\n"+
				"Extract the key information from this chunk: decisions made, file paths, function/symbol names, "+
				"commands run, errors hit, blockers, and unresolved questions. Preserve identifiers VERBATIM. "+
				"Under 300 words. This is a partial slice — a later pass will combine all chunk extracts into a final summary.\n\n"+
				"--- CHUNK ---\n%s",
			i+1, len(chunks), stepName, t.ID, t.Title, chunk,
		)
		log.Printf("summarize_chat: map step %d/%d for step %q of task #%d (%d chars)", i+1, len(chunks), stepName, t.ID, len(chunk))
		s, err := e.runSummarizerSync(ctx, mapPrompt, t.WorktreePath, "summarize_chat_chunk")
		if err != nil {
			return nil, fmt.Errorf("summarize_chat map step %d/%d failed: %w", i+1, len(chunks), err)
		}
		summaries = append(summaries, strings.TrimSpace(s))
	}
	return summaries, nil
}

// splitOnLineBoundary splits content into chunks no larger than maxBytes, breaking
// only on newline boundaries so that line-delimited formats (e.g. JSONL) stay intact.
// A single line longer than maxBytes becomes its own (oversized) chunk.
func splitOnLineBoundary(content string, maxBytes int) []string {
	if len(content) <= maxBytes {
		return []string{content}
	}
	var chunks []string
	var cur strings.Builder
	for _, line := range strings.Split(content, "\n") {
		// +1 accounts for the newline that will be re-added before this line.
		if cur.Len() > 0 && cur.Len()+1+len(line) > maxBytes {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteByte('\n')
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

// summarizationDescription returns a human-readable description of the summarization
// approach being used for a task, suitable for logging.
func summarizationDescription(taskID int64, hasCustomPrompt bool, artifactNames []string, useDiffStat bool) string {
	if hasCustomPrompt {
		if len(artifactNames) > 0 {
			return fmt.Sprintf("Summarizing task #%d with custom prompt and artifacts: %s", taskID, strings.Join(artifactNames, ", "))
		}
		return fmt.Sprintf("Summarizing task #%d with custom prompt", taskID)
	}
	if len(artifactNames) > 0 {
		return fmt.Sprintf("Summarizing task #%d with artifacts: %s", taskID, strings.Join(artifactNames, ", "))
	}
	if useDiffStat {
		return fmt.Sprintf("Summarizing task #%d via git diff", taskID)
	}
	return fmt.Sprintf("Summarizing task #%d", taskID)
}

// RunWorktreeSetupCommand runs the configured worktree setup command, if any.
// The command is executed with the project root as the working directory.
// {{worktree_path}} in the command string is replaced with the actual worktree path.
func RunWorktreeSetupCommand(ctx context.Context, projectRoot, worktreePath, command string) error {
	if command == "" {
		return nil
	}

	// Replace template variable
	resolved := strings.ReplaceAll(command, "{{worktree_path}}", worktreePath)

	log.Printf("Running worktree setup command: %s", resolved)

	cmd := exec.CommandContext(ctx, "sh", "-c", resolved)
	cmd.Dir = projectRoot

	output, err := cmd.CombinedOutput()
	if len(output) > 0 {
		log.Printf("Worktree setup output:\n%s", string(output))
	}
	if err != nil {
		return fmt.Errorf("worktree setup command failed: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// RunWorktreeSetupCommands runs multiple worktree setup commands sequentially.
// Each command is executed with the project root as the working directory.
// {{worktree_path}} in command strings is replaced with the actual worktree path.
// Execution stops at the first failure.
func RunWorktreeSetupCommands(ctx context.Context, projectRoot, worktreePath string, commands []string) error {
	for i, command := range commands {
		if command == "" {
			continue
		}
		log.Printf("Running worktree setup command [%d/%d]: %s", i+1, len(commands), command)
		if err := RunWorktreeSetupCommand(ctx, projectRoot, worktreePath, command); err != nil {
			return fmt.Errorf("worktree setup command [%d/%d] failed: %w", i+1, len(commands), err)
		}
	}
	return nil
}

// ErrNoSummarizer is returned when a summarization pass is requested but no
// `summarizer:` command is configured. Callers treat it as a degradation:
// summaries are skipped with a warning (or the task fails when a step demands
// context via require_context).
var ErrNoSummarizer = errors.New("no summarizer configured: set a top-level `summarizer:` command in .sakusen.yml (run `sakusen init` in a fresh project for a scaffolded default)")

// runSummarizerSync runs the configured summarizer command synchronously with
// the prompt piped on stdin and captures its stdout. workDir sets the working
// directory so the command can access the task's worktree files. purpose tags
// the invocation via SAKUSEN_PURPOSE so stubs/scripts can route the call
// without parsing prompt text.
//
// The prompt is piped on stdin rather than passed as an argv positional — this
// sidesteps the OS ARG_MAX ceiling for very large chat logs.
func (e *Engine) runSummarizerSync(ctx context.Context, prompt string, workDir string, purpose string) (string, error) {
	if !e.cfg.Summarizer.Configured() {
		return "", ErrNoSummarizer
	}
	env := map[string]string{}
	if purpose != "" {
		env["SAKUSEN_PURPOSE"] = purpose
	}
	return runner.RunSync(ctx, e.cfg.Summarizer.Command, workDir, env, prompt)
}
