---
name: workflow
description: >
  Sortie's workflow engine: task execution, template resolution, system prompt injection,
  step context capture, and merge/conflict resolution. Use when editing files in
  internal/workflow/, working on step execution, prompt templates, step context I/O,
  loop conditions, summarizer, or on_complete actions.
---

# Workflow Engine

## Execution Flow (RunTask)

1. Create/reuse git worktree (skip if `task.Worktree == false`)
2. **Sync configured paths** via `SyncPathsToWorktree(srcRoot, dstRoot string, paths config.WorktreeSyncPathsConfig)` — copies/links `worktree-sync-paths` from project root
3. Run `RunWorktreeSetupCommand()` if configured (worktree-only)
4. `EnsureWorkDirs(worktreePath)` — create `.sortie/logs/`
5. Copy attached images to `.sortie/images/`
6. For each step (from `task.StepIndex`):
   - Collect step contexts from prior steps (fetched from `task_steps` DB table)
   - Build `TemplateContext`, resolve prompt via `ResolveTemplate()`
   - `BuildSystemPrompt()` constructs system prompt string (passed via `--system-prompt` flag)
   - Spawn Claude (direct or tmux mode)
   - Capture `result` event from Claude's NDJSON output stream as step context
   - Store step context in `task_steps` DB table
   - Validate meaningful code changes (skip for human/tmux)
   - Evaluate loop conditions, check approval gates
7. Execute `on_complete` (commit/merge/none), run summarizer, clean up worktree (if merge)

## File Map

| File | Purpose |
|------|---------|
| `engine.go` | Core orchestrator: `Engine` struct, `NewEngine()`, `RunTask()`, `ResumeAfterApproval()` |
| `step.go` | Claude step execution: `runClaudeStep()`, `runClaudeStepTmux()`, `writeTmuxLogMessage()` |
| `merge.go` | Engine-side glue to `internal/merge`: `executeOnComplete()` (calls `e.coord.Finalize()`), `bindConflictResolver()` (wires Claude-driven resolver into the Coordinator), `resolveConflicts()` (the resolver itself), `cleanupMergedWorktree()`. **Per-repo locking, retry, and target-clean wait live in `internal/merge`, not here.** |
| `hooks.go` | Stop-hook installation in worktrees: `InstallStopHook()`, `SortieSettingsDir()`, `StepDoneDir()`, shell-quoting helpers |
| `summarizer.go` | Summarization + finalization: `FinalizeTask()`, `runSummarizer()`, `summarizeChatLog()`, `RunWorktreeSetupCommand()`, `runClaudeSync()` |
| `template.go` | `{{placeholder}}` interpolation via `ResolveTemplate()` |
| `system-prompt.go` | `BuildSystemPrompt()` — builds system prompt string for spawned Claude agents |
| `artifact.go` | Directory management, image copying |
| `sync.go` | `SyncPathsToWorktree(srcRoot, dstRoot string, paths config.WorktreeSyncPathsConfig) error` — copies/links configured paths |

## Template System

See [references/templates.md](references/templates.md) for supported placeholders and context struct.

## System Prompt Injection

`BuildSystemPrompt(resolvedPrompt, systemPrompt, imageRelPaths)` returns a string containing:

```
[systemPrompt or default autonomous agent prompt]
---
# Task
[resolvedPrompt]
## Attached Images
- image paths
```

This string is passed to Claude via the `--system-prompt` flag rather than written to a file,
preventing task-specific instructions from leaking into git history.

## Directory Structure

```
worktree/.sortie/
  images/image.png           Attached images
  logs/step_name.log         Per-worktree step logs
  step-prompt-*.txt          Prompt files for tmux steps
  run-step-*.sh              Wrapper scripts for tmux steps
```

Project-level logs: `.sortie/logs/{taskID}/{stepName}.log`

## Directory Functions

```go
LogsDir(worktreePath string) string
LogPath(worktreePath, stepName string) string
EnsureWorkDirs(worktreePath string) error
ProjectLogsDir(dataDir string, taskID int64) string
ProjectLogPath(dataDir string, taskID int64, stepName string) string
ImagesDir(worktreePath string) string
CopyImagesToWorktree(worktreePath string, imagePaths []string) ([]string, error)
```

## Non-Worktree Mode

When `task.Worktree == false`:
- Worktree creation and branch resolution are skipped; `WorktreePath` is set to project root
- Path syncing (`SyncPathsToWorktree`) is skipped
- `on_complete: merge` falls back to a simple commit (no branch to merge)
- Worktree/branch cleanup on delete is skipped
- The summarizer uses `git diff --stat` against `base_branch` for context (may be empty if changes were already committed)

## Finalization (FinalizeTask)

`FinalizeTask()` handles tmux task completion:
1. Runs `executeOnComplete` (commit/merge/none) — merges first to unblock user
2. Sets `StatusSummarizing`, runs summarizer
3. Cleans up worktree via `cleanupMergedWorktree` (if merge was performed)
4. Called from `handleAdvanceTask` → `runFinalization` (async)

## Key Mechanisms

- **Merge**: delegated to `internal/merge`. The Engine calls `e.coord.Finalize(ctx, t, baseBranch, logFn)`; the Coordinator owns per-repo serialization (via `*merge.Lock` from the daemon's `*merge.Locks` registry), `--no-ff` merge into base (preserves task branch commit history), Claude-driven conflict resolution (wired via `bindConflictResolver()`), up to 3 retries, target-clean wait, and cleanup-on-failure.
- **Loops**: evaluate at step end, check `MaxIterations` + `ExitCondition.StepContextEmpty`, persist iteration to DB
- **Approval gates**: human steps pause at `AwaitingApproval`, tmux steps at `Tmux`
- **Summarization strategy**: per-step `summarization_strategy` controls how step context is captured. `summarize_chat` (default when unset, see `StepConfig.EffectiveSummarizationStrategy()`) stores last_message immediately, then synchronously runs `summarizeChatLog()` against the chat JSONL and overwrites the context via `UpdateTaskStepContext()`. `last_message` keeps only Claude's result-event text — cheaper but loses decisions; for tmux steps it leaves context empty because there is no result event. Non-tmux + non-empty result text + chat < `smallChatBytes` (4 KB) short-circuits via `shouldSummarizeChat()` and keeps the result text. For tmux steps the summarization runs synchronously inside `ResumeAfterApproval` (the step itself returns immediately to pause at the tmux approval gate).
- **Model auto-selection**: callers do not name a model — they pass the per-step or project-level `allowed_summarization_models` allowlist (see `StepConfig.EffectiveAllowedSummarizationModels(e.cfg.AllowedSummarizationModels)`). `chooseSummarizationModel(promptBytes, allowed)` picks the cheapest allowed model whose ceiling (`maxPromptBytesForModel`: haiku 380 KB, sonnet 700 KB, opus 1500 KB — empirically calibrated, see `summarizer.go`) fits the resolved prompt. When nothing fits, `summarizeChatLog` falls back to a map-reduce pass on the largest allowed model: `splitOnLineBoundary` chunks the chat at `chunkBytesForModel(model)` boundaries, each chunk is summarised with a generic extraction prompt (each chunk re-runs auto-selection so smaller chunks may use a cheaper model), and the chunk summaries are fed back through the original (custom or default) prompt as `{{chat}}`. To skip map-reduce entirely on a large-transcript step, set `allowed_summarization_models: [opus]` so the selector is forced to opus's 1500 KB ceiling.
- **Claude transport**: `runClaudeSync` pipes the prompt on stdin (not argv) so the macOS ARG_MAX 1 MB limit does not cap the largest model. `claude` reads stdin via the default `--input-format text` path.
- **Environment**: `SORTIE_TASK_ID`, `SORTIE_STEP`, `SORTIE_WORKTREE` (+ `SORTIE_TRACK_ID` when the task has a track — also set for merge-conflict agents)

## Patterns

- Always use `TemplateContext` + `ResolveTemplate()` for prompt interpolation
- Step context captured from Claude's NDJSON `result` event and stored in `task_steps` table
- Step index persisted to DB after each step for crash recovery
- Tmux steps are fire-and-forget; daemon monitors session state separately
