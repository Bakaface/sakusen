// Package merge is the deep module that owns the per-repo merge invariant.
//
// "All merges into the same repo must serialize, and Sakusen never discards
// changes or switches branches in the target checkout." The merge is a
// fast-forward that git refuses rather than clobbering edits; if the target
// checkout is dirty or on another branch, the merge waits in merge-blocked.
// The target checkout is the main checkout, or — when the target branch is a
// task's own branch, e.g. a child merging into its parent — that task's
// worktree, provided the task is idle.
//
// This package consolidates the pieces of that invariant. The daemon hands
// out per-repo Locks via a Locks registry that survives config reloads; an
// Engine wraps a Lock plus a few engine-local callbacks (conflict resolution,
// DB writes) into a Coordinator; callers invoke Coordinator.Finalize and get
// the whole pipeline.
//
// The external interface is intentionally tiny:
//
//	coord.Finalize(ctx, task, baseBranch, log)
//
// Everything else — locking, conflict retry, target-branch wait — is
// locality inside this package.
package merge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	gitpkg "github.com/Bakaface/sakusen/internal/git"
	"github.com/Bakaface/sakusen/internal/task"
)

// Action is the on_complete behaviour the coordinator should run.
//
// "" and "none" are equivalent and produce a no-op. "commit" commits any
// pending changes in the task worktree. "merge" runs the full merge pipeline.
type Action = string

const (
	ActionNone   Action = "none"
	ActionCommit Action = "commit"
	ActionMerge  Action = "merge"
)

// Config holds the per-coordinator knobs. These are stable across tasks; the
// per-task action and branch are passed to Finalize.
type Config struct {
	// MaxAttempts caps the merge-retry loop. Defaults to 3 when zero.
	MaxAttempts int

	// BlockedPollInterval is how often to re-check whether the target checkout is
	// clean and on the target branch while waiting in merge-blocked state.
	// Defaults to 10s when zero.
	BlockedPollInterval time.Duration

	// CleanTargetTimeout fails the merge when the target checkout is still
	// dirty or off the target branch this long after the task entered
	// merge-blocked. Zero waits forever.
	CleanTargetTimeout time.Duration

	// IgnorePaths are git glob pathspecs, relative to the repo root, whose
	// changes do not count as a dirty target checkout.
	IgnorePaths []string

	// VerifyCommand, when set, runs via `sh -c` in the task worktree before
	// the merge, and again whenever a rebase onto the base branch moved the
	// task tip, so only a verified tip is ever fast-forwarded. A non-zero
	// exit or timeout fails the merge with ErrVerifyFailed.
	VerifyCommand string

	// VerifyTimeout bounds one VerifyCommand run. Zero means no limit.
	VerifyTimeout time.Duration
}

// ErrTargetDirtyTimeout is returned (wrapped) when the target checkout stays
// dirty or off the target branch past Config.CleanTargetTimeout.
var ErrTargetDirtyTimeout = errors.New("target checkout still dirty")

// maxBlockingPathsShown caps how many blocking paths a status detail lists.
const maxBlockingPathsShown = 10

// LogFunc is the per-call log sink used by Finalize. nil is treated as discard.
type LogFunc func(format string, args ...any)

// ConflictResolver is invoked when an in-worktree update against the base
// branch produces conflicts. Implementations typically spawn a Claude agent
// against the worktree to fix the conflict markers and stage the result.
//
// Returning nil means the conflicts are resolved and the merge commit can
// proceed. Returning an error aborts the retry loop.
type ConflictResolver func(ctx context.Context, t *task.Task, conflictFiles []string) error

// StatusSetter records a status change for a task. It is invoked outside the
// per-repo lock so it can safely take its own database mutexes.
type StatusSetter func(taskID int64, status task.Status) error

// StatusDetailSetter records a status change together with a human-readable
// detail (persisted as the task's error message; "" clears it).
type StatusDetailSetter func(taskID int64, status task.Status, detail string) error

// BlockedNotifier is invoked once each time a task enters merge-blocked, with
// the paths that block it.
type BlockedNotifier func(t *task.Task, paths []string)

// CommitRecorder appends a commit hash to a task's history.
type CommitRecorder func(taskID int64, commitHash string) error

// TargetWorktree is a Sakusen task worktree that may have the merge target
// branch checked out.
type TargetWorktree struct {
	TaskID int64
	Path   string
	Status task.Status
}

// TargetWorktreeLister returns the task worktrees whose task branch is
// branch, for the merge of t.
type TargetWorktreeLister func(t *task.Task, branch string) ([]TargetWorktree, error)

// gitRepo is the narrow set of *git.Repo operations the Coordinator depends
// on. Defining it here — rather than depending on *git.Repo directly —
// lets tests substitute a fake and exercise the merge/conflict
// pipeline without a real git binary. NewCoordinator always wires up the
// real *git.Repo for production use; only this package's fake-backed unit
// test and the internal newCoordinator constructor use the interface
// directly.
type gitRepo interface {
	Commit(worktreePath, message string) error
	MergeBranchIn(dir, branch string) error
	AbortRebase(worktreePath string) error
	RebaseInto(worktreePath, baseBranch string) error
	GetConflictedFiles(worktreePath string) ([]string, error)
	ContinueRebase(worktreePath string) error
	ChangedPathsIn(dir string, ignore []string) ([]string, error)
	GetCurrentBranch(dir string) (string, error)
	GetLastCommitHash(dir string) (string, error)
	Root() string
}

// Coordinator owns the merge pipeline for one repository: it serializes via a
// shared Lock, waits for the target checkout to be clean and on the target
// branch, and runs the merge with conflict-retry.
//
// One Coordinator instance exists per Engine; the Lock is shared across all
// Engines pointing at the same repoRoot (so config reloads do not break
// serialization).
type Coordinator struct {
	repo gitRepo
	lock *Lock
	cfg  Config

	resolveConflicts ConflictResolver
	setStatus        StatusSetter
	recordCommit     CommitRecorder
	setStatusDetail  StatusDetailSetter
	onBlocked        BlockedNotifier
	targetWorktrees  TargetWorktreeLister
}

// NewCoordinator wires a Lock plus its engine-local callbacks into a
// Coordinator backed by a real *git.Repo rooted at repoRoot. Any callback may
// be nil — a nil ConflictResolver means conflicts abort the merge instead of
// triggering resolution; nil StatusSetter / CommitRecorder /
// StatusDetailSetter / BlockedNotifier mean those side effects are skipped.
func NewCoordinator(
	repoRoot string,
	lock *Lock,
	cfg Config,
	resolveConflicts ConflictResolver,
	setStatus StatusSetter,
	recordCommit CommitRecorder,
	setStatusDetail StatusDetailSetter,
	onBlocked BlockedNotifier,
) *Coordinator {
	return newCoordinator(gitpkg.NewRepo(repoRoot), lock, cfg, resolveConflicts, setStatus, recordCommit, setStatusDetail, onBlocked)
}

// newCoordinator wires an arbitrary gitRepo implementation into a
// Coordinator. Only NewCoordinator (real *git.Repo) and this package's
// fake-backed unit test call it directly — production code should always go
// through NewCoordinator.
func newCoordinator(
	repo gitRepo,
	lock *Lock,
	cfg Config,
	resolveConflicts ConflictResolver,
	setStatus StatusSetter,
	recordCommit CommitRecorder,
	setStatusDetail StatusDetailSetter,
	onBlocked BlockedNotifier,
) *Coordinator {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.BlockedPollInterval <= 0 {
		cfg.BlockedPollInterval = 10 * time.Second
	}
	if lock == nil {
		lock = &Lock{}
	}
	return &Coordinator{
		repo:             repo,
		lock:             lock,
		cfg:              cfg,
		resolveConflicts: resolveConflicts,
		setStatus:        setStatus,
		recordCommit:     recordCommit,
		setStatusDetail:  setStatusDetail,
		onBlocked:        onBlocked,
	}
}

// SetTargetWorktrees lets merges land in a task worktree that has the target
// branch checked out. Without a lister only the main checkout takes merges.
func (c *Coordinator) SetTargetWorktrees(fn TargetWorktreeLister) { c.targetWorktrees = fn }

// Lock exposes the per-repo lock for ad-hoc operations that must serialize
// against merges (currently: revert and worktree teardown). Prefer Finalize.
func (c *Coordinator) Lock() *Lock { return c.lock }

// RepoRoot returns the absolute path this Coordinator merges into.
func (c *Coordinator) RepoRoot() string { return c.repo.Root() }

// Finalize runs the configured on_complete action for the task.
//
// Locality of the merge invariant:
//   - Serialization: the per-repo Lock is held only across the base-repo merge
//     operation itself. Worktree-side work (commits, rebase, conflict
//     resolution) runs outside the lock so two tasks can prep their worktrees
//     in parallel.
//   - Target checkout safety: the target checkout (main, or an idle task
//     worktree on baseBranch) is never reset or switched. Readiness (clean
//     and on baseBranch) is re-checked under the lock; if it doesn't hold,
//     the merge waits in merge-blocked instead of proceeding.
//
// The action ("merge" | "commit" | "none" | "") is resolved per-task by the
// caller (workflow-level override falling back to the project-level setting)
// and passed in explicitly.
//
// For action == "" / "none", or for non-worktree tasks, Finalize is a no-op.
// For action == "merge" on a non-worktree task, Finalize falls back to a
// commit (because there is no task branch to merge from).
func (c *Coordinator) Finalize(ctx context.Context, t *task.Task, baseBranch string, action Action, logFn LogFunc) error {
	logf := normalizeLog(logFn)

	switch action {
	case "", ActionNone:
		logf("No on_complete action configured, skipping")
		return nil
	}

	if !t.Worktree {
		// Non-worktree mode shares the user's working tree, so auto-committing
		// would scoop up unrelated changes and merge has no separate branch.
		// Leave the working tree alone.
		logf("No-worktree mode: leaving working tree as-is, skipping on_complete=%q", action)
		return nil
	}

	switch action {
	case ActionCommit:
		return c.commit(t, logf)
	case ActionMerge:
		return c.merge(ctx, t, baseBranch, logf)
	default:
		logf("Unknown on_complete action: %s", action)
		return nil
	}
}

// commit handles on_complete=commit by committing pending changes in the
// worktree. The base repo is not touched, so no locking or cleanup is needed.
func (c *Coordinator) commit(t *task.Task, logf LogFunc) error {
	logf("Committing changes in worktree...")
	if err := c.repo.Commit(t.WorktreePath, gitpkg.ConventionalCommitFromTitle(t.Title)); err != nil {
		return err
	}
	logf("Commit completed")
	c.recordWorktreeCommit(t)
	return nil
}

// merge runs the merge pipeline. The merge is always a fast-forward of the
// base branch onto the task tip. When the base branch has advanced past the
// task's fork point, the merge attempt fails and refreshFromBase rebases the
// task branch onto the latest base before retrying. This keeps history linear
// — no synthetic merge commits get added for either the refresh or the
// integration step.
func (c *Coordinator) merge(ctx context.Context, t *task.Task, baseBranch string, logf LogFunc) error {
	if t.Branch == "" {
		return errors.New("cannot merge: task branch name is empty")
	}

	// Commit any uncommitted worktree changes. Safe to do outside the per-repo
	// lock — this writes only to the worktree's index.
	logf("Committing changes in worktree before merge...")
	if err := c.repo.Commit(t.WorktreePath, gitpkg.ConventionalCommitFromTitle(t.Title)); err != nil {
		return fmt.Errorf("commit failed: %w", err)
	}

	if err := c.verify(ctx, t, logf); err != nil {
		return err
	}

	// Wait outside the lock so other tasks can keep merging meanwhile.
	logf("Checking target checkout is clean and on %s...", baseBranch)
	if err := c.waitForReadyTarget(ctx, t, baseBranch, logf); err != nil {
		return fmt.Errorf("waiting for target branch: %w", err)
	}

	logf("Fast-forwarding %s to branch %s...", baseBranch, t.Branch)

	var mergeErr error
	for attempt := 1; attempt <= c.cfg.MaxAttempts; attempt++ {
		if attempt > 1 {
			logf("Merge attempt %d/%d...", attempt, c.cfg.MaxAttempts)
		}

		var target string
		target, mergeErr = c.lockedMerge(t, baseBranch)
		if errors.Is(mergeErr, errTargetNotReady) {
			// The target checkout changed since the wait; this is not
			// a merge failure, so wait again without consuming an attempt.
			logf("%v", mergeErr)
			if err := c.waitForReadyTarget(ctx, t, baseBranch, logf); err != nil {
				return fmt.Errorf("waiting for target branch: %w", err)
			}
			attempt--
			continue
		}
		if mergeErr == nil {
			logf("Merge successful")
			c.recordBaseCommit(t, target)
			return nil
		}

		log.Printf("Merge attempt %d/%d failed for task #%d: %v", attempt, c.cfg.MaxAttempts, t.ID, mergeErr)
		logf("Merge attempt %d/%d failed: %v", attempt, c.cfg.MaxAttempts, mergeErr)

		if attempt == c.cfg.MaxAttempts {
			break
		}

		// Fast-forward failed because the base branch advanced past the
		// task's fork point. Rebase the task branch onto the latest base
		// (outside the lock) so the next attempt can fast-forward cleanly.
		if err := c.refreshAndReverify(ctx, t, baseBranch, logf); err != nil {
			return err
		}
	}

	return fmt.Errorf("merge failed after %d attempts: %w", c.cfg.MaxAttempts, mergeErr)
}

// lockedMerge is the only place the per-repo Lock is held. Keep it tiny —
// every line that runs under the lock contributes to head-of-line blocking
// across all tasks in this repo. Returns the checkout that was fast-forwarded.
func (c *Coordinator) lockedMerge(t *task.Task, baseBranch string) (string, error) {
	c.lock.Acquire()
	defer c.lock.Release()
	// Re-check under the lock: the target checkout may have been edited,
	// switched, or (for a task worktree) resumed since waitForReadyTarget
	// returned.
	target, blocker, _, err := c.targetCheckout(t, baseBranch)
	if err != nil {
		return "", err
	}
	if blocker != "" {
		return "", fmt.Errorf("%w: %s", errTargetNotReady, blocker)
	}
	return target, c.repo.MergeBranchIn(target, t.Branch)
}

// errTargetNotReady means the target checkout is dirty, not on the base
// branch, or owned by a busy task, so the merge must wait rather than
// proceed.
var errTargetNotReady = errors.New("target not ready")

// targetCheckout resolves the checkout the merge into baseBranch lands in and
// whether it can take the merge right now. The main checkout is the target
// when it is on baseBranch; otherwise a task worktree that has baseBranch
// checked out is (see targetWorktree). A non-empty blocker says why the merge
// must wait; when it is uncommitted changes (outside Config.IgnorePaths,
// untracked files included), the blocking paths are returned too.
func (c *Coordinator) targetCheckout(t *task.Task, baseBranch string) (dir, blocker string, paths []string, err error) {
	root := c.repo.Root()
	branch, err := c.repo.GetCurrentBranch(root)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to check target branch: %w", err)
	}
	dir = root
	dirtyPrefix := "target checkout"
	if branch != baseBranch {
		wt, wtBlocker, err := c.targetWorktree(t, baseBranch)
		if err != nil {
			return "", "", nil, err
		}
		if wtBlocker != "" {
			return "", wtBlocker, nil, nil
		}
		if wt == nil {
			if branch == "HEAD" {
				return "", fmt.Sprintf("main checkout is on a detached HEAD instead of %s", baseBranch), nil, nil
			}
			return "", fmt.Sprintf("main checkout is on branch %s instead of %s", branch, baseBranch), nil, nil
		}
		dir = wt.Path
		dirtyPrefix = fmt.Sprintf("target worktree of task #%d", wt.TaskID)
	}
	paths, err = c.repo.ChangedPathsIn(dir, c.cfg.IgnorePaths)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to check target branch: %w", err)
	}
	if len(paths) > 0 {
		return dir, blockedDetail(dirtyPrefix, paths), paths, nil
	}
	return dir, "", nil, nil
}

// targetWorktree finds the task worktree (other than t's own) that has
// baseBranch checked out, or nil when none does. A non-empty blocker means it
// exists but its task is not idle, so it must not be touched yet.
func (c *Coordinator) targetWorktree(t *task.Task, baseBranch string) (*TargetWorktree, string, error) {
	if c.targetWorktrees == nil {
		return nil, "", nil
	}
	candidates, err := c.targetWorktrees(t, baseBranch)
	if err != nil {
		return nil, "", fmt.Errorf("failed to look up task worktrees on %s: %w", baseBranch, err)
	}
	for i := range candidates {
		wt := &candidates[i]
		if wt.TaskID == t.ID || wt.Path == "" {
			continue
		}
		// Git checks a branch out in at most one worktree. A stale path
		// (removed worktree) or one switched to another branch is not it.
		branch, err := c.repo.GetCurrentBranch(wt.Path)
		if err != nil || branch != baseBranch {
			continue
		}
		if !acceptsMerges(wt.Status) {
			return nil, fmt.Sprintf("%s is checked out in the worktree of task #%d, which is %s", baseBranch, wt.TaskID, wt.Status), nil
		}
		return wt, "", nil
	}
	return nil, "", nil
}

// acceptsMerges reports whether a task's worktree may be fast-forwarded: only
// while no step, finalization or interactive session of that task can be
// writing to it.
func acceptsMerges(s task.Status) bool {
	switch s {
	case task.StatusAwaitingChildren, task.StatusAwaitingApproval,
		task.StatusCompleted, task.StatusFailed, task.StatusMergeFailed:
		return true
	case task.StatusPending, task.StatusInit, task.StatusRunning, task.StatusTmux,
		task.StatusFinalizing, task.StatusSummarizing, task.StatusSummarizingStep,
		task.StatusMergeBlocked, task.StatusResolvingConflicts:
		return false
	}
	return false
}

// refreshAndReverify runs refreshFromBase and, when a verify command is
// configured and the rebase moved the task tip, verifies the new tip.
func (c *Coordinator) refreshAndReverify(ctx context.Context, t *task.Task, baseBranch string, logf LogFunc) error {
	if c.cfg.VerifyCommand == "" {
		return c.refreshFromBase(ctx, t, baseBranch, logf)
	}
	before, beforeErr := c.repo.GetLastCommitHash(t.WorktreePath)
	if err := c.refreshFromBase(ctx, t, baseBranch, logf); err != nil {
		return err
	}
	after, afterErr := c.repo.GetLastCommitHash(t.WorktreePath)
	if beforeErr == nil && afterErr == nil && before == after {
		return nil
	}
	return c.verify(ctx, t, logf)
}

// refreshFromBase rebases the task's worktree branch onto baseBranch,
// resolving any per-commit conflicts via the injected ConflictResolver.
// Returns nil on success, leaving the branch ready for a fast-forward
// merge into baseBranch.
//
// Runs outside the per-repo lock — the worktree has its own index and
// other tasks' merges into base do not need to block on this.
func (c *Coordinator) refreshFromBase(ctx context.Context, t *task.Task, baseBranch string, logf LogFunc) (retErr error) {
	logf("Rebasing branch onto %s...", baseBranch)

	// Safety net: any non-nil return must leave the worktree out of rebase
	// state, so a later attempt does not encounter a stale .git/rebase-merge
	// directory.
	defer func() {
		if retErr != nil {
			if err := c.repo.AbortRebase(t.WorktreePath); err != nil {
				log.Printf("Warning: task #%d: %v", t.ID, err)
			}
		}
	}()

	err := c.repo.RebaseInto(t.WorktreePath, baseBranch)
	for err != nil {
		conflictFiles, cfErr := c.repo.GetConflictedFiles(t.WorktreePath)
		if cfErr != nil {
			return fmt.Errorf("could not list conflicts: %w", cfErr)
		}
		if len(conflictFiles) == 0 {
			return fmt.Errorf("rebase failed with no conflicted files: %w", err)
		}
		if c.resolveConflicts == nil {
			return fmt.Errorf("rebase produced %d conflicted files and no resolver is configured", len(conflictFiles))
		}

		log.Printf("Task #%d has %d conflicted files during rebase, invoking resolver", t.ID, len(conflictFiles))
		logf("Found %d conflicted files, invoking resolver...", len(conflictFiles))

		// Surface the conflict-resolution phase via the task status so the TUI can
		// distinguish it from regular step execution (and avoid showing a stale
		// "implementing [wip]" label while the conflict agent is running).
		restoreStatus := c.markResolvingConflicts(t)
		resolveErr := c.resolveConflicts(ctx, t, conflictFiles)
		restoreStatus()
		if resolveErr != nil {
			return fmt.Errorf("rebase conflict resolution failed: %w", resolveErr)
		}

		remaining, listErr := c.repo.GetConflictedFiles(t.WorktreePath)
		if listErr != nil {
			return fmt.Errorf("failed to verify conflict resolution: %w", listErr)
		}
		if len(remaining) > 0 {
			return fmt.Errorf("resolver left %d files still conflicted: %v", len(remaining), remaining)
		}

		log.Printf("Task #%d conflicts resolved, continuing rebase", t.ID)
		logf("Conflicts resolved, continuing rebase...")

		// Continue may surface conflicts on a subsequent commit — the loop
		// runs the resolver again for those.
		err = c.repo.ContinueRebase(t.WorktreePath)
	}

	return nil
}

// markResolvingConflicts transitions the task into StatusResolvingConflicts
// and returns a function that restores the previous status. Used to surface
// the conflict-resolution phase in the TUI without persisting
// "resolving-conflicts" as the resting status of the task.
//
// On any DB failure the returned function is a no-op (logged) — surfacing the
// status is best-effort and must not block conflict resolution.
func (c *Coordinator) markResolvingConflicts(t *task.Task) func() {
	if c.setStatus == nil {
		return func() {}
	}
	prev := t.Status
	if err := c.setStatus(t.ID, task.StatusResolvingConflicts); err != nil {
		log.Printf("Warning: failed to set resolving-conflicts status for task #%d: %v", t.ID, err)
		return func() {}
	}
	t.Status = task.StatusResolvingConflicts
	return func() {
		if err := c.setStatus(t.ID, prev); err != nil {
			log.Printf("Warning: failed to restore status %q for task #%d after conflict resolution: %v", prev, t.ID, err)
			return
		}
		t.Status = prev
	}
}

// waitForReadyTarget polls until the target checkout is on baseBranch with no
// pending changes outside Config.IgnorePaths. While waiting the task sits in
// merge-blocked with the blocker (wrong branch, or the blocking paths) as its
// status detail; once ready the prior status is restored and the detail
// cleared. Runs outside any lock — the goal is to let other tasks finish
// their merges.
func (c *Coordinator) waitForReadyTarget(ctx context.Context, t *task.Task, baseBranch string, logf LogFunc) error {
	_, blocker, paths, err := c.targetCheckout(t, baseBranch)
	if err != nil {
		return err
	}
	if blocker == "" {
		return nil
	}

	prev := t.Status
	blockedAt := time.Now()
	log.Printf("Task #%d: %s, entering merge-blocked state", t.ID, blocker)
	logf("Merge blocked: %s", blocker)
	c.writeStatusDetail(t, task.StatusMergeBlocked, blocker)
	if c.onBlocked != nil {
		c.onBlocked(t, paths)
	}

	ticker := time.NewTicker(c.cfg.BlockedPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_, latest, _, err := c.targetCheckout(t, baseBranch)
			if err != nil {
				log.Printf("Task #%d: %v", t.ID, err)
				continue
			}
			if latest == "" {
				log.Printf("Task #%d: target checkout is clean and on %s, proceeding with merge", t.ID, baseBranch)
				c.writeStatusDetail(t, prev, "")
				return nil
			}
			if c.cfg.CleanTargetTimeout > 0 && time.Since(blockedAt) >= c.cfg.CleanTargetTimeout {
				return fmt.Errorf("%w after %s: %s", ErrTargetDirtyTimeout, c.cfg.CleanTargetTimeout, latest)
			}
			if latest != blocker {
				blocker = latest
				logf("Merge blocked: %s", blocker)
				c.writeStatusDetail(t, task.StatusMergeBlocked, blocker)
			}
			log.Printf("Task #%d: %s, retrying", t.ID, blocker)
		}
	}
}

// writeStatusDetail persists status+detail and mirrors the status onto t.
// Best-effort: a DB failure is logged and never interrupts the wait.
func (c *Coordinator) writeStatusDetail(t *task.Task, status task.Status, detail string) {
	if c.setStatusDetail == nil {
		return
	}
	if err := c.setStatusDetail(t.ID, status, detail); err != nil {
		log.Printf("Warning: failed to set %s status for task #%d: %v", status, t.ID, err)
		return
	}
	t.Status = status
}

func blockedDetail(checkout string, paths []string) string {
	return checkout + " has uncommitted changes: " + joinBlockingPaths(paths)
}

// joinBlockingPaths renders at most maxBlockingPathsShown paths, summarizing
// the rest as ", and N more".
func joinBlockingPaths(paths []string) string {
	if len(paths) <= maxBlockingPathsShown {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(paths[:maxBlockingPathsShown], ", "), len(paths)-maxBlockingPathsShown)
}

// recordBaseCommit appends the new HEAD of the fast-forwarded target
// checkout to the task's commit history.
func (c *Coordinator) recordBaseCommit(t *task.Task, target string) {
	if c.recordCommit == nil {
		return
	}
	hash, err := c.repo.GetLastCommitHash(target)
	if err != nil {
		log.Printf("Warning: failed to get base HEAD hash for task #%d: %v", t.ID, err)
		return
	}
	if err := c.recordCommit(t.ID, hash); err != nil {
		log.Printf("Warning: failed to record base commit for task #%d: %v", t.ID, err)
	}
}

// recordWorktreeCommit appends the just-created worktree commit (HEAD of the
// worktree) to the task's commit history.
func (c *Coordinator) recordWorktreeCommit(t *task.Task) {
	if c.recordCommit == nil {
		return
	}
	hash, err := c.repo.GetLastCommitHash(t.WorktreePath)
	if err != nil {
		log.Printf("Warning: failed to get commit hash for task #%d: %v", t.ID, err)
		return
	}
	if err := c.recordCommit(t.ID, hash); err != nil {
		log.Printf("Warning: failed to record commit for task #%d: %v", t.ID, err)
	}
}

func normalizeLog(fn LogFunc) LogFunc {
	if fn != nil {
		return fn
	}
	return func(string, ...any) {}
}

// Lock is the per-repo merge serializer. It wraps sync.Mutex so the surface is
// explicit ("Acquire merge" / "Release merge") rather than the more generic
// Lock/Unlock.
type Lock struct {
	mu sync.Mutex
}

// Acquire blocks until the caller can be the exclusive merger for this repo.
func (l *Lock) Acquire() { l.mu.Lock() }

// Release returns the merge lock so the next waiter can proceed.
func (l *Lock) Release() { l.mu.Unlock() }

// WithLock runs fn while holding the merge lock. Prefer this over
// Acquire/Release in callers that just want to do a small protected block, so
// the lock cannot leak on a panic.
func (l *Lock) WithLock(fn func()) {
	l.Acquire()
	defer l.Release()
	fn()
}

// Locks is the daemon-owned registry that hands out one Lock per repo root.
// Locks survive engine reconstruction (config reloads), so concurrent tasks
// always agree on a single serializer.
type Locks struct {
	mu    sync.Mutex
	locks map[string]*Lock
}

// NewLocks returns an empty registry.
func NewLocks() *Locks {
	return &Locks{locks: make(map[string]*Lock)}
}

// For returns the Lock for the given repo root, creating one on first use.
// Two calls with the same repoRoot always return the same *Lock.
func (l *Locks) For(repoRoot string) *Lock {
	l.mu.Lock()
	defer l.mu.Unlock()
	lk, ok := l.locks[repoRoot]
	if !ok {
		lk = &Lock{}
		l.locks[repoRoot] = lk
	}
	return lk
}
