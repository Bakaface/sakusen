package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/task"
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
// .claude-output.log, CLAUDE.md). Ownership of that list stays with the
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

	// Run Claude synchronously to capture the summary text. Auto-select a
	// model from the project-level allowlist based on prompt size — the final
	// task summarizer uses the same allowlist as step-level summarize_chat
	// (per-step allowlist overrides only apply to summarize_chat passes).
	model, _ := chooseSummarizationModel(len(prompt), e.cfg.AllowedSummarizationModels)
	summary, err := e.runClaudeSync(ctx, prompt, t.WorktreePath, "summarize", model)
	if err != nil {
		return fmt.Errorf("summarizer claude invocation failed: %w", err)
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

// encodeClaudeProjectDir encodes a workdir path to the directory name format used by
// Claude Code under ~/.claude/projects/. Claude Code replaces every non-alphanumeric
// character (e.g. '/', '.', '_', spaces) with '-', preserving case and NOT collapsing
// runs of separators. Replacing only '/' and '.' would mis-encode paths containing
// underscores (e.g. "uscreen_2" → "uscreen-2"), pointing at a non-existent JSONL and
// silently dropping the chat content for tmux steps.
func encodeClaudeProjectDir(workdir string) string {
	var b strings.Builder
	b.Grow(len(workdir))
	for _, r := range workdir {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// loadStepChatContent returns the raw chat content for a step.
// For tmux steps, reads the Claude session JSONL via the session id recorded by UpsertChat.
// For headless steps, reads the per-step log file.
// Returns empty string (no error) if no content is available yet.
func (e *Engine) loadStepChatContent(t *task.Task, stepName string, useTmux bool) (string, error) {
	if useTmux {
		// Look up the session id recorded when the tmux step started
		chat, err := e.database.GetChatByStep(t.ID, stepName)
		if err != nil {
			return "", fmt.Errorf("failed to look up chat session for tmux step %q: %w", stepName, err)
		}
		if chat == nil || chat.SessionID == "" {
			// No session recorded yet — treat as no content available
			return "", nil
		}

		// Construct the JSONL path: ~/.claude/projects/<encoded-workdir>/<sessionid>.jsonl
		encoded := encodeClaudeProjectDir(t.WorktreePath)
		jsonlPath := filepath.Join(os.Getenv("HOME"), ".claude", "projects", encoded, chat.SessionID+".jsonl")
		data, err := os.ReadFile(jsonlPath)
		if err != nil {
			if os.IsNotExist(err) {
				// A session id was persisted for this step, so its transcript
				// JSONL must exist somewhere. A missing file means the path we
				// derived does not match where Claude Code actually stored the
				// session (e.g. a project-dir encoding mismatch). Surface it
				// loudly — silently returning "" here drops the entire step's
				// chat and leaves the step context empty with no breadcrumb.
				log.Printf("Warning: recorded session %q for tmux step %q of task #%d has no JSONL at %s; step context will be empty", chat.SessionID, stepName, t.ID, jsonlPath)
				return "", nil
			}
			return "", fmt.Errorf("failed to read claude session JSONL for step %q: %w", stepName, err)
		}
		transcript, hasConversation := extractSessionTranscript(string(data))
		if !hasConversation {
			// The session JSONL exists but holds no actual assistant turn — only the
			// injected step prompt plus metadata lines (mode, permission-mode,
			// file-history-snapshot, attachment, ...). This happens when a tmux step
			// is (re-)spawned but no conversation runs in it (e.g. the step is
			// restarted and advanced without anyone grilling). Feeding the raw prompt
			// to summarize_chat makes the summarizer RE-ENACT the prompt's embedded
			// instructions instead of summarizing — confabulating a bogus context
			// (e.g. emitting the grilling agent's opening question as the "summary").
			// Treat it as no content so the caller's empty-guard fires: a prior
			// manually-folded context is preserved, and require_context fails loudly,
			// instead of overwriting good context with garbage.
			log.Printf("Step %q of task #%d: session %q has no conversational turns; treating as empty chat", stepName, t.ID, chat.SessionID)
			return "", nil
		}
		return transcript, nil
	}

	// Headless step: slice the most recent run of this step out of the unified
	// task log. The step header and footer (written by runClaudeStep) act as
	// region markers; retries leave multiple header/footer pairs in the file
	// and we want the most recent.
	logPath := ProjectLogPath(e.dataDir, t.ID)
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to read task log for step %q: %w", stepName, err)
	}
	return extractLatestStepRegion(string(data), stepName), nil
}

// sessionEntry decodes the fields we need from one line of a Claude Code
// interactive session JSONL transcript (the per-session files under
// ~/.claude/projects/<encoded>/<id>.jsonl). These files interleave conversational
// turns ("user"/"assistant") with many non-conversational line types — mode,
// permission-mode, file-history-snapshot, attachment, last-prompt, ai-title,
// queue-operation — which are ignored.
type sessionEntry struct {
	Type        string          `json:"type"`
	IsSidechain bool            `json:"isSidechain"`
	Message     *sessionMessage `json:"message"`
}

type sessionMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // a plain string OR an array of blocks
}

type sessionBlock struct {
	Type    string          `json:"type"` // "text", "tool_use", "tool_result", "thinking"
	Text    string          `json:"text"`
	Name    string          `json:"name"`    // tool name for tool_use
	Input   json.RawMessage `json:"input"`   // tool_use input
	Content json.RawMessage `json:"content"` // tool_result content (string OR array of {text})
}

// extractSessionTranscript parses a Claude Code session JSONL file into a clean,
// human-readable transcript, dropping the non-conversational metadata lines and the
// verbose internals (thinking blocks, raw tool I/O) that would otherwise bloat the
// summarizer prompt and tempt the model into re-enacting embedded instructions
// rather than summarizing.
//
// It returns the rendered transcript and whether the session contains any actual
// assistant turn. A session with no assistant turn carries no conversation worth
// summarizing — only the injected step prompt — so callers treat hasConversation
// == false as "no chat content".
func extractSessionTranscript(raw string) (string, bool) {
	var b strings.Builder
	hasAssistant := false
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e sessionEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if e.Message == nil || (e.Type != "user" && e.Type != "assistant") {
			continue
		}
		rendered := renderSessionContent(e.Message.Content)
		if rendered == "" {
			continue
		}
		role := e.Type
		if e.IsSidechain {
			role = "subagent-" + role
		}
		if e.Type == "assistant" {
			hasAssistant = true
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.ToUpper(role[:1]) + role[1:])
		b.WriteString(": ")
		b.WriteString(rendered)
	}
	return b.String(), hasAssistant
}

// renderSessionContent renders a session message's content, which is either a plain
// string (typical user message) or an array of content blocks (assistant turns and
// tool-result user turns). Thinking blocks are dropped; tool calls/results are
// rendered as compact, truncated markers so identifiers and error strings survive
// without dragging full file dumps into the summarizer prompt.
func renderSessionContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var blocks []sessionBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, blk := range blocks {
		switch blk.Type {
		case "text":
			if t := strings.TrimSpace(blk.Text); t != "" {
				parts = append(parts, t)
			}
		case "tool_use":
			if in := truncateTranscript(strings.TrimSpace(string(blk.Input)), 200); in != "" {
				parts = append(parts, fmt.Sprintf("[tool: %s %s]", blk.Name, in))
			} else {
				parts = append(parts, fmt.Sprintf("[tool: %s]", blk.Name))
			}
		case "tool_result":
			if r := strings.TrimSpace(renderToolResult(blk.Content)); r != "" {
				parts = append(parts, "[tool result: "+truncateTranscript(r, 500)+"]")
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// renderToolResult flattens a tool_result block's content, which Claude Code stores
// as either a plain string or an array of {"type":"text","text":...} blocks.
func renderToolResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []sessionBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, blk := range blocks {
		if t := strings.TrimSpace(blk.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

// truncateTranscript collapses a value to a single-line, length-bounded form for the
// transcript. Newlines become spaces so tool I/O stays on one marker line.
func truncateTranscript(s string, maxLen int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

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

// smallChatBytes is the threshold below which a non-tmux step skips the haiku
// summarization pass and keeps the Claude result-event text as step context.
// Roughly equates to "a handful of NDJSON events" — too short to be worth
// paying a haiku round trip for.
const smallChatBytes = 4096

// shouldSummarizeChat returns true when the chat log is worth running a haiku
// summarization pass over. For non-tmux steps with a non-empty result text and
// a tiny chat log, the result text is kept and haiku is skipped. Tmux steps
// always summarize because they have no result-event fallback.
func shouldSummarizeChat(chat, resultText string, useTmux bool) bool {
	if useTmux {
		return true
	}
	if strings.TrimSpace(resultText) == "" {
		return true
	}
	return len(chat) >= smallChatBytes
}

// haikuPromptByteLimit / sonnetPromptByteLimit / opusPromptByteLimit are the
// empirically measured safe per-invocation prompt-size ceilings for `claude -p`.
// Each is calibrated below the size at which `claude -p` returns
// "Prompt is too long" — see scripts/measure-claude-limits/ (or
// /tmp/sortie-limit-test/results.csv from the original measurement). The prompt
// is piped on stdin so the OS-level ARG_MAX (1 MB on macOS) does not apply.
const (
	haikuPromptByteLimit  = 380 * 1024  // empirical reject at ~420 KB
	sonnetPromptByteLimit = 700 * 1024  // empirical reject at ~800 KB
	opusPromptByteLimit   = 1500 * 1024 // empirical reject at ~1.8 MB
)

// maxPromptBytesForModel returns the safe upper bound for a single `claude -p`
// invocation when using the given model alias. Unknown aliases fall back to
// the haiku ceiling (most conservative).
func maxPromptBytesForModel(model string) int {
	switch model {
	case config.SummarizationModelOpus:
		return opusPromptByteLimit
	case config.SummarizationModelSonnet:
		return sonnetPromptByteLimit
	default:
		return haikuPromptByteLimit
	}
}

// chunkBytesForModel returns the target chunk size for map-reduce summarization
// when using the given model, leaving headroom for the surrounding instruction
// prompt.
func chunkBytesForModel(model string) int {
	const headroom = 30 * 1024
	return maxPromptBytesForModel(model) - headroom
}

// chooseSummarizationModel picks the cheapest model from the allowed list
// whose prompt-byte ceiling fits promptBytes. Returns (model, fits=true) when
// a fitting model exists. If no allowed model can hold the prompt, returns the
// largest-ceiling allowed model and fits=false — the caller should fall back
// to map-reduce on the returned model.
//
// Model ordering (cheapest → most capable): haiku < sonnet < opus. An empty
// allowed list is treated as DefaultAllowedSummarizationModels.
func chooseSummarizationModel(promptBytes int, allowed []string) (string, bool) {
	if len(allowed) == 0 {
		allowed = config.DefaultAllowedSummarizationModels
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, m := range allowed {
		allowedSet[m] = true
	}
	// Cheapest → most capable.
	candidates := []string{
		config.SummarizationModelHaiku,
		config.SummarizationModelSonnet,
		config.SummarizationModelOpus,
	}
	var largestAllowed string
	var largestCap int
	for _, m := range candidates {
		if !allowedSet[m] {
			continue
		}
		cap := maxPromptBytesForModel(m)
		if promptBytes <= cap {
			return m, true
		}
		if cap > largestCap {
			largestCap = cap
			largestAllowed = m
		}
	}
	// Nothing fits — fall back to the largest allowed model for map-reduce.
	// largestAllowed is non-empty because allowed is non-empty after the
	// default-list substitution above (and candidates covers every entry that
	// passes ValidateSteps).
	return largestAllowed, false
}

// summarizeChatLog summarises the given chat content via Claude. The model is
// chosen automatically per-call: the cheapest model in `allowed` whose
// prompt-size ceiling fits the resolved prompt wins (see
// chooseSummarizationModel). If no allowed model fits, the chat is summarised
// via map-reduce on the largest allowed model: split on line boundaries into
// chunks sized to fit, each chunk is summarised with a generic extraction
// prompt, then the chunk summaries are fed back through the original
// (customPrompt or default) final-summary prompt — at which point a fresh
// auto-selection runs over the (smaller) reduced prompt.
//
// customPrompt is a template that may reference the chat via a {{chat}}
// placeholder; task template variables ({{task.id}}, {{task.title}}, etc.) are
// also resolved. If customPrompt is empty, the default summarization prompt is
// used.
//
// An empty `allowed` list falls back to DefaultAllowedSummarizationModels.
func (e *Engine) summarizeChatLog(ctx context.Context, t *task.Task, stepName, customPrompt, chatContent string, allowed []string) (string, error) {
	if strings.TrimSpace(chatContent) == "" {
		return "", nil
	}

	prompt := e.buildSummarizePrompt(t, stepName, customPrompt, chatContent)
	model, fits := chooseSummarizationModel(len(prompt), allowed)

	if !fits {
		log.Printf("summarize_chat: prompt %d bytes exceeds all allowed-model limits (%v) for step %q of task #%d; running map-reduce on %s", len(prompt), allowed, stepName, t.ID, model)
		chunkSummaries, err := e.summarizeChatChunks(ctx, t, stepName, chatContent, allowed, model)
		if err != nil {
			return "", err
		}
		reduced := strings.Join(chunkSummaries, "\n\n--- CHUNK BOUNDARY ---\n\n")
		prompt = e.buildSummarizePrompt(t, stepName, customPrompt, reduced)
		// Re-select on the smaller reduced prompt: a cheaper model may now fit.
		model, _ = chooseSummarizationModel(len(prompt), allowed)
		log.Printf("summarize_chat: map-reduce reduce step for step %q of task #%d (%d chunk summaries, %d chars, model=%s)", stepName, t.ID, len(chunkSummaries), len(reduced), model)
	}

	log.Printf("Running summarize_chat for step %q of task #%d (model=%s, prompt %d bytes)", stepName, t.ID, model, len(prompt))
	summary, err := e.runClaudeSync(ctx, prompt, t.WorktreePath, "summarize_chat", model)
	if err != nil {
		return "", fmt.Errorf("summarize_chat claude invocation failed: %w", err)
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
		"Summarize the following Claude Code conversation log from step %q of task #%d: %s\n\n"+
			"Output requirements:\n"+
			"- Under 200 words.\n"+
			"- Preserve file paths, function/symbol names, command lines, and error strings VERBATIM — do not paraphrase identifiers.\n"+
			"- Cover what was accomplished, key decisions, files changed, and any blockers or unresolved issues.\n"+
			"- Prioritise actionable detail over narrative; this summary becomes context for later workflow steps.\n\n"+
			"--- CONVERSATION LOG ---\n%s",
		stepName, t.ID, t.Title, chatContent,
	)
}

// summarizeChatChunks splits chatContent on line boundaries (sized for
// chunkModel) and runs an extraction pass over each chunk, returning the
// per-chunk summaries. Each chunk re-selects from `allowed`: chunks small
// enough to fit a cheaper model use it. chunkModel sets the chunk size and
// caps the maximum chunk a cheaper model may need to swallow.
func (e *Engine) summarizeChatChunks(ctx context.Context, t *task.Task, stepName, chatContent string, allowed []string, chunkModel string) ([]string, error) {
	chunks := splitOnLineBoundary(chatContent, chunkBytesForModel(chunkModel))
	summaries := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		mapPrompt := fmt.Sprintf(
			"This is chunk %d of %d from a Claude Code conversation log (step %q of task #%d: %s).\n"+
				"Extract the key information from this chunk: decisions made, file paths, function/symbol names, "+
				"commands run, errors hit, blockers, and unresolved questions. Preserve identifiers VERBATIM. "+
				"Under 300 words. This is a partial slice — a later pass will combine all chunk extracts into a final summary.\n\n"+
				"--- CHUNK ---\n%s",
			i+1, len(chunks), stepName, t.ID, t.Title, chunk,
		)
		model, _ := chooseSummarizationModel(len(mapPrompt), allowed)
		log.Printf("summarize_chat: map step %d/%d for step %q of task #%d (model=%s, %d chars)", i+1, len(chunks), stepName, t.ID, model, len(chunk))
		s, err := e.runClaudeSync(ctx, mapPrompt, t.WorktreePath, "summarize_chat_chunk", model)
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

// runClaudeSync runs Claude Code synchronously and captures its stdout output.
// workDir sets the working directory for the Claude process so it can access
// the task's worktree files. purpose tags the invocation via SORTIE_PURPOSE so
// stub claude binaries can route the response without parsing prompt text.
// model is the Claude model alias ("haiku", "sonnet", "opus") or full model id;
// an empty string falls back to the haiku alias.
//
// The prompt is piped on stdin (claude reads it via the default --input-format
// text path) rather than passed as an argv positional. This sidesteps the
// macOS ARG_MAX (1 MB) ceiling that would otherwise cap the largest model
// (opus) far below its actual prompt-size capacity.
func (e *Engine) runClaudeSync(ctx context.Context, prompt string, workDir string, purpose string, model string) (string, error) {
	if model == "" {
		model = config.SummarizationModelHaiku
	}
	args := []string{"-p", "--output-format", "text", "--model", model}
	args = append(args, e.cfg.Claude.Args()...)

	cmd := exec.CommandContext(ctx, e.cfg.Claude.Command, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	if purpose != "" {
		cmd.Env = append(os.Environ(), "SORTIE_PURPOSE="+purpose)
	}
	cmd.Stdin = strings.NewReader(prompt)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// claude prints user-facing errors (e.g. "Prompt is too long") to stdout
		// rather than stderr, so surface both streams for diagnosis.
		return "", fmt.Errorf("claude command failed: %w (stdout: %s, stderr: %s)", err, truncateForLog(stdout.String()), truncateForLog(stderr.String()))
	}

	return stdout.String(), nil
}

// truncateForLog clips a string to a sensible length for inclusion in error
// messages so a multi-megabyte stdout dump cannot drown a log line.
func truncateForLog(s string) string {
	const maxLen = 500
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + fmt.Sprintf("... (truncated, %d total bytes)", len(s))
}
