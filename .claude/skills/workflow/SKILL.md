---
name: workflow
description: >
  Sortie's workflow engine: task execution, template resolution, agent spawning,
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
   - Build `TemplateContext`, resolve prompt via `ResolveTemplate()` (attached images append an "## Attached Images" section to the step prompt itself)
   - Resolve the step's agent record via `cfg.StepAgent(wf, &step)`; its mode picks headless vs tmux execution
   - Headless: write the prompt to `.sortie/step-prompt-<step>.txt`, spawn the agent command via `runner.Process` with `SORTIE_PROMPT_FILE`/`SORTIE_RESULT_FILE` exported, capture the result text from `$SORTIE_RESULT_FILE` (stdout-tail fallback)
   - Tmux: write a wrapper script (`runner.BuildWrapperScript`) exporting the sentinel contract (`SORTIE_DONE_DIR`/`SORTIE_DONE_PREFIX`) and return immediately
   - Store step context in `task_steps` DB table
   - Validate meaningful code changes (skip for human/tmux)
   - Evaluate loop conditions, check approval gates
7. Execute `on_complete` (commit/merge/none), run summarizer, clean up worktree (if merge)

## File Map

| File | Purpose |
|------|---------|
| `engine.go` | Core orchestrator: `Engine` struct, `NewEngine()`, `RunTask()`, `runStep()`, `ResumeAfterApproval()`, `summarizePreviousTmuxStep()` |
| `step.go` | Agent step execution: the `agentRunner` test seam, `runHeadlessAgent()` (spawns `runner.Process`), `runStepTmux()`, `writeTmuxLogMessage()` |
| `sentinel.go` | Generic turn-end sentinel convention for tmux steps: `StepDoneDir()`, `SentinelPrefix()`, `StepSentinel` payload (`session_id`/`transcript_path`), `LatestStepSentinel(WithPath)()`, `ClearStepSentinels()` |
| `stepcontext.go` | Step-context precedence (manual > last_message > summarize_chat): `captureHeadlessStepContext()`, `PublishManualStepContext()`, `RecordTmuxStepSentinelSession()` |
| `merge.go` | Engine-side glue to `internal/merge`: `executeOnComplete()` (calls `e.coord.Finalize()`), `bindConflictResolver()` (wires the agent-driven resolver into the Coordinator), `resolveConflicts()` (the resolver itself), `cleanupMergedWorktree()`. **Per-repo locking, retry, and target-clean wait live in `internal/merge`, not here.** |
| `summarizer.go` | Summarization + finalization: `FinalizeTask()`, `runSummarizer()`, `summarizeChatLog()`, `loadStepChatContent()`, `RunWorktreeSetupCommand()`, `runSummarizerSync()` |
| `template.go` | `{{placeholder}}` interpolation via `ResolveTemplate()` |
| `artifact.go` | Directory management, image copying |
| `sync.go` | `SyncPathsToWorktree(srcRoot, dstRoot string, paths config.WorktreeSyncPathsConfig) error` — copies/links configured paths |

## Template System

See [references/templates.md](references/templates.md) for supported placeholders and context struct.

## Agent Spawning Contract

There is no system-prompt injection — the fully-resolved step prompt is written to
`.sortie/step-prompt-<step>.txt` and the agent command reads it via `$SORTIE_PROMPT_FILE`.
Attached images append an "## Attached Images" section to the step prompt itself. The engine
exports the env contract (`SORTIE_TASK_ID`, `SORTIE_STEP`, `SORTIE_WORKTREE`,
`SORTIE_PROJECT_PATH`, `SORTIE_PURPOSE`, `SORTIE_AGENT`, `SORTIE_TRACK_ID` when tracked) plus
`SORTIE_PROMPT_FILE`/`SORTIE_RESULT_FILE` (headless) or
`SORTIE_DONE_DIR`/`SORTIE_DONE_PREFIX` (tmux); the agent record's `env:` map is merged
underneath via `runner.MergeEnv` (the contract wins).

The `agentRunner` interface in `step.go` is the headless test seam: tests override
`Engine.runner` with a fake returning scripted `(exitCode, resultText, outputTail, err)`.
The tmux path is deliberately not seamed (fire-and-forget; see the doc comment).

## Directory Structure

```
worktree/.sortie/
  images/image.png           Attached images
  logs/                      Created by EnsureWorkDirs
  step-prompt-*.txt          Resolved step prompts (SORTIE_PROMPT_FILE)
  step-result-*.txt          Headless result files (SORTIE_RESULT_FILE)
  step-done/                 Turn-end sentinel files for tmux steps (SORTIE_DONE_DIR)
  run-step-*.sh              Wrapper scripts for tmux steps
```

Project-level unified task log: `.sortie/logs/{taskID}/task.log` — all steps and finalization
append to this single file in chronological order.

## Directory Functions

```go
LogsDir(worktreePath string) string
EnsureWorkDirs(worktreePath string) error
ProjectLogsDir(dataDir string, taskID int64) string
ProjectLogPath(dataDir string, taskID int64) string   // unified per-task log (task.log)
ImagesDir(worktreePath string) string
CopyImagesToWorktree(worktreePath string, imagePaths []string) ([]string, error)
StepDoneDir(worktreePath string) string               // sentinel dir (sentinel.go)
SentinelPrefix(stepName string) string                // sentinel filename prefix
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

- **Merge**: delegated to `internal/merge`. The Engine calls `e.coord.Finalize(ctx, t, baseBranch, onComplete, logFn)`; the Coordinator owns per-repo serialization (via `*merge.Lock` from the daemon's `*merge.Locks` registry), `--no-ff` merge into base (preserves task branch commit history), agent-driven conflict resolution (wired via `bindConflictResolver()`), up to 3 retries, target-clean wait, and cleanup-on-failure. The conflict resolver requires a HEADLESS agent: the workflow's agent, or the implicit `"claude"` fallback when the workflow agent is tmux-mode; it errors when only tmux agents exist.
- **Loops**: evaluate at step end, check `MaxIterations` + `ExitCondition.StepContextEmpty`, persist iteration to DB
- **Approval gates**: human steps pause at `AwaitingApproval`, tmux steps at `Tmux`
- **Summarization strategy**: per-step `summarization_strategy` controls how step context is captured. `summarize_chat` (default when unset, see `StepConfig.EffectiveSummarizationStrategy()`) stores last_message immediately, then synchronously runs `summarizeChatLog()` against the step's chat content and overwrites the context via `UpdateTaskStepContext()`. Chat content comes from `loadStepChatContent()`: headless steps slice the step's region out of the unified task log; tmux steps run the step agent's `chat_log_command` (env: `SORTIE_SESSION_ID` from the chats table, `SORTIE_SENTINEL_FILE` + `SORTIE_TRANSCRIPT_PATH` from the latest sentinel) and use its stdout — an agent without `chat_log_command` degrades to no chat content (warn, or fail when `require_context: true`). `last_message` keeps only the headless result text — cheaper but loses decisions; for tmux steps it leaves context empty because there is no result text. Non-tmux + non-empty result text + chat < `smallChatBytes` (4 KB) short-circuits via `shouldSummarizeChat()` and keeps the result text. For tmux steps the summarization runs synchronously inside `ResumeAfterApproval` (the step itself returns immediately to pause at the tmux approval gate).
- **Summarizer command**: `runSummarizerSync(ctx, prompt, workDir, purpose)` runs the configured `cfg.Summarizer.Command` via `runner.RunSync` — prompt on stdin (sidesteps ARG_MAX for huge chat logs), response on stdout, `SORTIE_PURPOSE=<purpose>` set. Returns `ErrNoSummarizer` when no `summarizer:` command is configured — callers degrade (skip with a warning) or fail the task when the step sets `require_context: true`. Map-reduce chunking is gated on `Summarizer.MaxPromptBytes` (0 = no chunking): `splitOnLineBoundary` chunks at `maxBytes - chunkHeadroomBytes` (30 KiB headroom), each chunk gets a generic extraction pass (`SORTIE_PURPOSE=summarize_chat_chunk`), and the chunk summaries are fed back through the original (custom or default) prompt.
- **Environment**: `SORTIE_TASK_ID`, `SORTIE_STEP`, `SORTIE_WORKTREE`, `SORTIE_PROJECT_PATH`, `SORTIE_PURPOSE`, `SORTIE_AGENT` (+ `SORTIE_TRACK_ID` when the task has a track — also set for merge-conflict agents, which use `SORTIE_PURPOSE=merge_conflict`)

## Patterns

- Always use `TemplateContext` + `ResolveTemplate()` for prompt interpolation
- Step context captured from the headless agent's result text (`$SORTIE_RESULT_FILE`, stdout-tail fallback) and stored in `task_steps` table
- Step index persisted to DB after each step for crash recovery
- Tmux steps are fire-and-forget; the daemon monitors sentinels/session state separately (no Stop-hook install in core — hookless agents are manual-advance)
