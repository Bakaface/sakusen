---
name: workflow
description: >
  Sakusen's workflow engine: task execution, template resolution, agent spawning,
  step context capture, and merge/conflict resolution. Use when editing files in
  internal/workflow/, working on step execution, prompt templates, step context I/O,
  loop conditions, summarizer, or on_complete actions.
---

# Workflow Engine

## Execution Flow (RunTask)

1. Create/reuse git worktree (skip if `task.Worktree == false`)
2. **Sync configured paths** via `SyncPathsToWorktree(srcRoot, dstRoot string, paths config.WorktreeSyncPathsConfig)` — copies/links `worktree-sync-paths` from project root
3. Run `RunWorktreeSetupCommand()` if configured (worktree-only)
4. `EnsureWorkDirs(worktreePath)` — create `.sakusen/logs/`
5. Copy attached images to `.sakusen/images/`
6. For each step (from `task.StepIndex`):
   - Collect step contexts from prior steps (fetched from `task_steps` DB table)
   - Build `TemplateContext`, resolve prompt via `ResolveTemplate()` (attached images append an "## Attached Images" section to the step prompt itself)
   - Resolve the step's agent record via `cfg.StepAgent(wf, &step)`; its mode picks headless vs tmux execution
   - Headless: write the prompt to `.sakusen/step-prompt-<step>.txt`, spawn the agent command via `runner.Process` with `SAKUSEN_PROMPT_FILE`/`SAKUSEN_RESULT_FILE` exported, capture the result text from `$SAKUSEN_RESULT_FILE` (stdout-tail fallback)
   - Tmux: write a wrapper script (`runner.BuildWrapperScript`) exporting the sentinel contract (`SAKUSEN_DONE_DIR`/`SAKUSEN_DONE_PREFIX`) and return immediately
   - Store step context in `task_steps` DB table
   - Validate meaningful code changes (skip for human/tmux)
   - Evaluate loop conditions, check approval gates
   - A step carrying `parallel:` short-circuits all of the above: `runStep` hands it to `runParallelGroup` right after the template context is built (a group has no prompt and no agent of its own)
7. Execute `on_complete` (commit/merge/none), run summarizer, clean up worktree (if merge)

## File Map

| File | Purpose |
|------|---------|
| `engine.go` | Core orchestrator: `Engine` struct, `NewEngine()`, `RunTask()`, `runStep()`, `ResumeAfterApproval()`, `summarizePreviousTmuxStep()` |
| `step.go` | Agent step execution: the `agentRunner` test seam, `headlessSpawn` (per-spawn log/output/tag overrides), `runHeadlessAgent()` (spawns `runner.Process`), `runStepTmux()`, `tagLines()`, `writeTmuxLogMessage()` |
| `parallel.go` | Parallel groups (fan-out / fan-in): `runParallelGroup()`, `runBranch()`, `captureBranchContext()`, `summarizeParallelBranches()`, `loadBranchChatContent()`, `BranchLogPath()`, `branchOutputFile()` |
| `sentinel.go` | Generic turn-end sentinel convention for tmux steps: `StepDoneDir()`, `SentinelPrefix()`, `StepSentinel` payload (`session_id`/`transcript_path`), `LatestStepSentinel(WithPath)()`, `ClearStepSentinels()` |
| `stepcontext.go` | Step-context precedence (manual > last_message > summarize_chat): `captureHeadlessStepContext()`, `PublishManualStepContext()`, `RecordTmuxStepSentinelSession()` |
| `merge.go` | Engine-side glue to `internal/merge`: `executeOnComplete()` (calls `e.coord.Finalize()`), `bindConflictResolver()` (wires the agent-driven resolver into the Coordinator), `resolveConflicts()` (the resolver itself), `cleanupMergedWorktree()`. **Per-repo locking, retry, and target-clean wait live in `internal/merge`, not here.** |
| `summarizer.go` | Summarization + finalization: `FinalizeTask()`, `runSummarizer()`, `summarizeChatLog()`, `loadStepChatContent()`, `RunWorktreeSetupCommand()`, `runSummarizerSync()` |
| `template.go` | `{{placeholder}}` interpolation via `ResolveTemplate()`; `{{prompt.<name>}}` file includes |
| `artifact.go` | Directory management, image copying |
| `sync.go` | `SyncPathsToWorktree(srcRoot, dstRoot string, paths config.WorktreeSyncPathsConfig) error` — copies/links configured paths |

## Template System

See [references/templates.md](references/templates.md) for supported placeholders and context struct.

`ResolveTemplate(tmpl, ctx) (string, error)` first expands `{{prompt.<name>}}` includes against
`ctx.PromptDirs` (`config.PromptDirs`: `<project>/.sakusen/prompts` then `~/.sakusen/prompts`),
then resolves every other placeholder over the combined text. It returns an error only for a
broken include — a missing file, a malformed name, or a nested `{{prompt.*}}` — and callers fail
the step / summarizer pass rather than shipping a half-resolved prompt.

## Agent Spawning Contract

There is no system-prompt injection — the fully-resolved step prompt is written to
`.sakusen/step-prompt-<step>.txt` and the agent command reads it via `$SAKUSEN_PROMPT_FILE`.
Attached images append an "## Attached Images" section to the step prompt itself. The engine
exports the env contract (`SAKUSEN_TASK_ID`, `SAKUSEN_STEP`, `SAKUSEN_WORKTREE`,
`SAKUSEN_PROJECT_PATH`, `SAKUSEN_PURPOSE`, `SAKUSEN_AGENT`, `SAKUSEN_TRACK_ID` when tracked) plus
`SAKUSEN_PROMPT_FILE`/`SAKUSEN_RESULT_FILE` (headless) or
`SAKUSEN_DONE_DIR`/`SAKUSEN_DONE_PREFIX` (tmux); the agent record's `env:` map is merged
underneath via `runner.MergeEnv` (the contract wins).

The `agentRunner` interface in `step.go` is the headless test seam: tests override
`Engine.runner` with a fake returning scripted `(exitCode, resultText, outputTail, err)`.
The tmux path is deliberately not seamed (fire-and-forget; see the doc comment).

## Directory Structure

```
worktree/.sakusen/
  images/image.png           Attached images
  logs/                      Created by EnsureWorkDirs
  step-prompt-*.txt          Resolved step prompts (SAKUSEN_PROMPT_FILE)
  step-result-*.txt          Headless result files (SAKUSEN_RESULT_FILE)
  step-done/                 Turn-end sentinel files for tmux steps (SAKUSEN_DONE_DIR)
  run-step-*.sh              Wrapper scripts for tmux steps
```

Project-level unified task log: `.sakusen/logs/{taskID}/task.log` — all steps and finalization
append to this single file in chronological order. Parallel BRANCHES are the one exception: each
writes `.sakusen/logs/{taskID}/branch-<name>.log` (identical region layout) and its raw capture to
`worktree/.sakusen/output-<name>.log`, while task.log gets only the two
`=== parallel <group>: ... ===` marker lines. The live output stream still carries every branch
line, tagged `[<branch>]` after the timestamp — so the live view and task.log deliberately differ
for the group's duration.

## Directory Functions

```go
LogsDir(worktreePath string) string
EnsureWorkDirs(worktreePath string) error
ProjectLogsDir(dataDir string, taskID int64) string
ProjectLogPath(dataDir string, taskID int64) string   // unified per-task log (task.log)
BranchLogPath(dataDir string, taskID int64, branch string) string // per-branch log (parallel.go)
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

- **Conflict prompt**: `resolveConflicts` renders ONE template — `merge_conflicts.prompt` when set, else the built-in `defaultConflictPrompt` — through `ResolveTemplate` with task, git and `{{conflict.files}}` vars (step/loop/children/track vars resolve empty, since no step ran). The synthetic `resolve-conflicts` step takes its timeout from `merge_conflicts.timeout` (default 10m).
- **Merge**: delegated to `internal/merge`. The Engine calls `e.coord.Finalize(ctx, t, baseBranch, onComplete, logFn)`; the Coordinator owns per-repo serialization (via `*merge.Lock` from the daemon's `*merge.Locks` registry), `--no-ff` merge into base (preserves task branch commit history), agent-driven conflict resolution (wired via `bindConflictResolver()`), up to 3 retries, target-clean wait, and cleanup-on-failure. The conflict resolver requires a HEADLESS agent, resolved by `config.Config.MergeConflictAgentFor` (`merge_conflicts.agent` → workflow agent → `default_agent` → `"claude"`): an explicit `merge_conflicts.agent` must itself be headless, while a tmux workflow agent falls back to the implicit `"claude"` record; it errors when only tmux agents exist.
- **Parallel groups**: `runParallelGroup` reads `GetTaskStepRows` (a branch whose row is already `completed` is NOT launched — it counts toward K and its stored context is reused), appends `=== parallel <group>: started N branches ===` to task.log, then runs one goroutine per launched branch under a `sync.WaitGroup` and a cancelable child context (no errgroup dependency). Each branch resolves its prompt from a COPY of the shared template context with `Branch` set (`{{branch.name}}`/`{{branch.agent}}`), exports `SAKUSEN_STEP=<branch>`, and spawns with a `headlessSpawn{LogPath: branch-<name>.log, OutputFile: .sakusen/output-<name>.log, Tag: <name>}`. A branch that fails gets `FailTaskStep` (status `failed`) and a short reason — `exit <code>`, `timed out after <timeout>`, `cancelled`, or the spawn error; under `require: all` the first failure cancels the siblings. After the join, `summarize_chat` branches are summarized SEQUENTIALLY (`summarizeParallelBranches`) from their own branch log. K = completed branches; below `RequiredCount()` the group fails the task and its row stays `running` (like any failed step). Otherwise the group returns `FormatParallelAggregate(...)` as its result text and `captureHeadlessStepContext` stores it verbatim.
- **Loops**: evaluate at step end, check `MaxIterations` + `ExitCondition.StepContextEmpty`, persist iteration to DB. A loop whose range spans a parallel group deletes that group's branch rows before jumping back (`clearParallelBranchRows`), so the next iteration re-runs every branch rather than skipping the completed ones
- **Approval gates**: human steps pause at `AwaitingApproval`, tmux steps at `Tmux`
- **Summarization strategy**: per-step `summarization_strategy` controls how step context is captured. `summarize_chat` (default when unset, see `StepConfig.EffectiveSummarizationStrategy()`) stores last_message immediately, then synchronously runs `summarizeChatLog()` against the step's chat content and overwrites the context via `UpdateTaskStepContext()`. Chat content comes from `loadStepChatContent()`: headless steps slice the step's region out of the unified task log and then keep **only the agent's streamed output** via `stepAgentOutput()` — the step header, the echoed prompt block (bounded by the single literally-empty line `runHeadlessAgent` writes after it), and the footer are stripped, because summarizing sakusen's own prompt back into the step context overwrites the agent's real result text, and `smallChatBytes` is no protection since a long prompt clears it. An agent that redirects stdout into `$SAKUSEN_RESULT_FILE` (the documented env contract) streams nothing, so this correctly yields `""` and the summarize pass is skipped. Tmux steps run the step agent's `chat_log_command` (env: `SAKUSEN_SESSION_ID` from the chats table, `SAKUSEN_SENTINEL_FILE` + `SAKUSEN_TRANSCRIPT_PATH` from the latest sentinel) and use its stdout — an agent without `chat_log_command` degrades to no chat content (warn, or fail when `require_context: true`). `last_message` keeps only the headless result text — cheaper but loses decisions; for tmux steps it leaves context empty because there is no result text. Non-tmux + non-empty result text + chat < `smallChatBytes` (4 KB) short-circuits via `shouldSummarizeChat()` and keeps the result text. For tmux steps the summarization runs synchronously inside `ResumeAfterApproval` (the step itself returns immediately to pause at the tmux approval gate).
- **Summarizer**: `runSummarizerSync(ctx, prompt, workDir, purpose)` resolves the configured runner (`config.SummarizerInvocation`) and hands it to the package-level `RunSummarizer`, shared with the daemon (titles/slugs) and `sakusen backfill-context`. An `agent:` runs through `runner.RunAgentSync` (prompt written to a scratch `$SAKUSEN_PROMPT_FILE`, answer read from `$SAKUSEN_RESULT_FILE` with a stdout-tail fallback, `SAKUSEN_PROJECT_PATH` + the agent's `env` exported); a bare `command:` runs through `runner.RunSync` (prompt on stdin — sidesteps ARG_MAX for huge chat logs — response on stdout). `SAKUSEN_PURPOSE=<purpose>` is set either way, and summarizer output is never streamed into the task log. Returns `ErrNoSummarizer` when no summarizer is configured — callers degrade (skip with a warning) or fail the task when the step sets `require_context: true`. Map-reduce chunking is gated on `Summarizer.MaxPromptBytes` (0 = no chunking): `splitOnLineBoundary` chunks at `maxBytes - chunkHeadroomBytes` (30 KiB headroom), each chunk gets a generic extraction pass (`SAKUSEN_PURPOSE=summarize_chat_chunk`), and the chunk summaries are fed back through the original (custom or default) prompt.
- **Environment**: `SAKUSEN_TASK_ID`, `SAKUSEN_STEP`, `SAKUSEN_WORKTREE`, `SAKUSEN_PROJECT_PATH`, `SAKUSEN_PURPOSE`, `SAKUSEN_AGENT` (+ `SAKUSEN_TRACK_ID` when the task has a track — also set for merge-conflict agents, which use `SAKUSEN_PURPOSE=merge_conflict`)

## Patterns

- Always use `TemplateContext` + `ResolveTemplate()` for prompt interpolation
- Step context captured from the headless agent's result text (`$SAKUSEN_RESULT_FILE`, stdout-tail fallback) and stored in `task_steps` table
- Step index persisted to DB after each step for crash recovery
- Tmux steps are fire-and-forget; the daemon monitors sentinels/session state separately (no Stop-hook install in core — hookless agents are manual-advance)
