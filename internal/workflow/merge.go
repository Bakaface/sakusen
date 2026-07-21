package workflow

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/task"
)

// executeOnComplete runs the configured on_complete action after all steps
// finish. All the locality of the merge invariant — serialization, retry on
// conflict, cleanup-on-failure, waiting for a clean target branch — lives in
// internal/merge. This wrapper exists only because the engine still needs to
// resolve the effective base branch and forward a log function; both are
// engine-level concerns.
func (e *Engine) executeOnComplete(ctx context.Context, t *task.Task, _ func([]string), logFn func(string, ...any)) error {
	return e.coord.Finalize(ctx, t, e.effectiveBaseBranch(t), e.effectiveOnComplete(t), logFn)
}

// bindConflictResolver returns a merge.ConflictResolver closure that spawns a
// Claude agent to fix conflict markers in the task worktree. The closure is
// captured once at Engine construction so the merge package never has to
// import workflow or claude.
func (e *Engine) bindConflictResolver() func(ctx context.Context, t *task.Task, conflictFiles []string) error {
	return func(ctx context.Context, t *task.Task, conflictFiles []string) error {
		return e.resolveConflicts(ctx, t, conflictFiles, nil)
	}
}

// resolveConflicts spawns a Claude Code agent to resolve merge conflicts in the worktree.
func (e *Engine) resolveConflicts(ctx context.Context, t *task.Task, conflictFiles []string, outputFn func([]string)) error {
	var sb strings.Builder
	sb.WriteString("You are resolving merge conflicts in an automated merge pipeline.\n\n")
	sb.WriteString(fmt.Sprintf("The branch `%s` is being merged into `%s`, and the following files have conflicts:\n\n", e.cfg.BaseBranch, t.Branch))
	for _, f := range conflictFiles {
		sb.WriteString(fmt.Sprintf("- `%s`\n", f))
	}
	sb.WriteString("\nYour job:\n")
	sb.WriteString("1. Open each conflicted file and resolve all `<<<<<<<`, `=======`, `>>>>>>>` conflict markers\n")
	sb.WriteString("2. Choose the correct resolution by understanding both sides of the conflict\n")
	sb.WriteString("3. Run `git add <file>` on each resolved file\n")
	sb.WriteString("4. Do NOT run `git commit` — the merge commit will be created automatically\n")
	sb.WriteString("5. Do NOT modify any files that are not conflicted\n")
	sb.WriteString("6. Verify the code compiles after resolving conflicts (run `go build ./...` or equivalent)\n")
	prompt := sb.String()

	conflictSysPrompt := BuildSystemPrompt(prompt, e.cfg.SystemPrompt, nil)

	step := config.StepConfig{
		Name:    "resolve-conflicts",
		Timeout: "10m",
	}

	env := map[string]string{
		"SORTIE_TASK_ID":  fmt.Sprintf("%d", t.ID),
		"SORTIE_STEP":     step.Name,
		"SORTIE_WORKTREE": t.WorktreePath,
		"SORTIE_PURPOSE":  "merge_conflict",
	}
	if t.TrackID != nil {
		// Parity with step agents — merge-conflict agents belong to the same task.
		env["SORTIE_TRACK_ID"] = fmt.Sprintf("%d", *t.TrackID)
	}

	exitCode, _, _, outputTail, err := e.runClaudeStep(ctx, t, step, prompt, env, outputFn, conflictSysPrompt)
	if err != nil {
		return fmt.Errorf("conflict resolution claude process failed: %w", err)
	}
	if exitCode != 0 {
		errMsg := fmt.Sprintf("conflict resolution exited with code %d", exitCode)
		if outputTail != "" {
			errMsg += "\n" + outputTail
		}
		return errors.New(errMsg)
	}

	return nil
}

// cleanupMergedWorktree removes the worktree and branch after a successful merge.
func (e *Engine) cleanupMergedWorktree(t *task.Task, logf func(string, ...any)) {
	logf("Cleaning up worktree and branch...")
	if err := e.repo.RemoveWorktree(t.WorktreePath); err != nil {
		log.Printf("Warning: failed to remove worktree: %v", err)
	}
	// Prune stale worktree references so git doesn't think the branch
	// is still checked out in a (now-removed) worktree.
	if err := e.repo.CleanupWorktrees(); err != nil {
		log.Printf("Warning: failed to prune worktrees: %v", err)
	}
	// Only delete branches that sortie created; preserve user-provided branches
	if t.CheckoutBranch == "" {
		if err := e.repo.ForceDeleteBranch(t.Branch); err != nil {
			log.Printf("Warning: failed to delete branch: %v", err)
		}
	}
	// Clear worktree path in DB
	if err := e.database.ClearWorktreePath(t.ID); err != nil {
		log.Printf("Warning: failed to clear worktree path: %v", err)
	}
	logf("Cleanup completed")
}
