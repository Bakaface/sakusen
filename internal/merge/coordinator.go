// Package merge is the deep module that owns the per-repo merge invariant.
//
// "All merges into the same repo must serialize, and on any failure the repo's
// working tree state must be cleaned." Before this module existed that
// invariant was spread across three packages — the daemon held the per-repo
// mutex map, the workflow engine drove the retry loop, and internal/git had a
// deferred CleanRepoState on the merge primitive. Each piece was shallow
// because it relied on the others to behave correctly.
//
// This package consolidates them. The daemon hands out per-repo Locks via a
// Locks registry that survives config reloads; an Engine wraps a Lock plus a
// few engine-local callbacks (conflict resolution, DB writes) into a
// Coordinator; callers invoke Coordinator.Finalize and get the whole pipeline.
//
// The external interface is intentionally tiny:
//
//	coord.Finalize(ctx, task, baseBranch, log)
//
// Everything else — locking, conflict retry, target-branch wait,
// cleanup-on-failure — is locality inside this package.
package merge

import (
	"context"
	"errors"
	"fmt"
	"log"
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

	// BlockedPollInterval is how often to re-check whether the target branch is
	// clean while waiting in merge-blocked state. Defaults to 10s when zero.
	BlockedPollInterval time.Duration
}

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

// CommitRecorder appends a commit hash to a task's history.
type CommitRecorder func(taskID int64, commitHash string) error

// gitRepo is the narrow set of *git.Repo operations the Coordinator depends
// on. Defining it here — rather than depending on *git.Repo directly —
// lets tests substitute a fake and exercise the merge/conflict/cleanup
// pipeline without a real git binary. NewCoordinator always wires up the
// real *git.Repo for production use; only this package's fake-backed unit
// test and the internal newCoordinator constructor use the interface
// directly.
type gitRepo interface {
	Commit(worktreePath, message string) error
	CleanRepoState() error
	MergeBranch(branch, baseBranch string) error
	AbortRebase(worktreePath string) error
	RebaseInto(worktreePath, baseBranch string) error
	GetConflictedFiles(worktreePath string) ([]string, error)
	ContinueRebase(worktreePath string) error
	HasChanges() (bool, error)
	GetLastCommitHash(dir string) (string, error)
	Root() string
}

// Coordinator owns the merge pipeline for one repository: it serializes via a
// shared Lock, waits for the target branch to be clean, runs the merge with
// conflict-retry, and guarantees CleanRepoState on any pipeline failure.
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
}

// NewCoordinator wires a Lock plus its engine-local callbacks into a
// Coordinator backed by a real *git.Repo rooted at repoRoot. Any callback may
// be nil — a nil ConflictResolver means conflicts abort the merge instead of
// triggering resolution; nil StatusSetter / CommitRecorder mean those side
// effects are skipped.
func NewCoordinator(
	repoRoot string,
	lock *Lock,
	cfg Config,
	resolveConflicts ConflictResolver,
	setStatus StatusSetter,
	recordCommit CommitRecorder,
) *Coordinator {
	return newCoordinator(gitpkg.NewRepo(repoRoot), lock, cfg, resolveConflicts, setStatus, recordCommit)
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
	}
}

// Lock exposes the per-repo lock for ad-hoc operations that must serialize
// against merges (currently: revert and worktree teardown). Prefer Finalize.
func (c *Coordinator) Lock() *Lock { return c.lock }

// RepoRoot returns the absolute path this Coordinator merges into.
func (c *Coordinator) RepoRoot() string { return c.repo.Root() }

// Finalize runs the configured on_complete action for the task and guarantees
// that on any failure during a merge the repository's working tree is restored
// to a clean state.
//
// Locality of the merge invariant:
//   - Serialization: the per-repo Lock is held only across the base-repo merge
//     operation itself. Worktree-side work (commits, rebase, conflict
//     resolution) runs outside the lock so two tasks can prep their worktrees
//     in parallel.
//   - Cleanup: CleanRepoState fires whenever the merge branch of this function
//     returns an error — regardless of whether the failure came from
//     MergeBranch, RebaseInto, ContinueRebase, or the conflict resolver.
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

// merge runs the merge pipeline with cleanup-on-failure as a top-level
// guarantee. The deferred CleanRepoState fires for any non-nil return inside
// this method — including failures that happen *after* MergeBranch has
// committed and *before* the post-merge bookkeeping completes.
//
// The merge is always a fast-forward of the base branch onto the task tip.
// When the base branch has advanced past the task's fork point, the merge
// attempt fails and refreshFromBase rebases the task branch onto the latest
// base before retrying. This keeps history linear — no synthetic merge
// commits get added for either the refresh or the integration step.
func (c *Coordinator) merge(ctx context.Context, t *task.Task, baseBranch string, logf LogFunc) (retErr error) {
	if t.Branch == "" {
		return errors.New("cannot merge: task branch name is empty")
	}

	// Top-level safety net: if any branch of the merge pipeline returns an
	// error, abort any in-progress merge and hard-reset the base repo so a
	// later task does not see staged conflict markers on main. This is the
	// single owner of the cleanup invariant — internal/git's MergeBranch keeps
	// its own deferred CleanRepoState as defence-in-depth, but the
	// authoritative cleanup lives here so failures *outside* MergeBranch
	// (conflict resolution, target-clean wait, etc.) are covered too.
	defer func() {
		if retErr == nil {
			return
		}
		if err := c.repo.CleanRepoState(); err != nil {
			log.Printf("merge.Coordinator: failed to clean repo state for %s after merge failure: %v", c.repo.Root(), err)
			logf("Warning: cleanup after merge failure failed: %v", err)
		}
	}()

	// Commit any uncommitted worktree changes. Safe to do outside the per-repo
	// lock — this writes only to the worktree's index.
	logf("Committing changes in worktree before merge...")
	if err := c.repo.Commit(t.WorktreePath, gitpkg.ConventionalCommitFromTitle(t.Title)); err != nil {
		return fmt.Errorf("commit failed: %w", err)
	}

	// Wait for the target branch to be clean. Done outside the lock so other
	// tasks can finish their own merges (which is what clears the dirty state).
	logf("Checking target branch %s for pending changes...", baseBranch)
	if err := c.waitForCleanTarget(ctx, t); err != nil {
		return fmt.Errorf("waiting for clean target branch: %w", err)
	}

	logf("Fast-forwarding %s to branch %s...", baseBranch, t.Branch)

	var mergeErr error
	for attempt := 1; attempt <= c.cfg.MaxAttempts; attempt++ {
		if attempt > 1 {
			logf("Merge attempt %d/%d...", attempt, c.cfg.MaxAttempts)
		}

		mergeErr = c.lockedMerge(t.Branch, baseBranch)
		if mergeErr == nil {
			logf("Merge successful")
			c.recordBaseCommit(t)
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
		if err := c.refreshFromBase(ctx, t, baseBranch, logf); err != nil {
			return err
		}
	}

	return fmt.Errorf("merge failed after %d attempts: %w", c.cfg.MaxAttempts, mergeErr)
}

// lockedMerge is the only place the per-repo Lock is held. Keep it tiny —
// every line that runs under the lock contributes to head-of-line blocking
// across all tasks in this repo.
func (c *Coordinator) lockedMerge(branch, baseBranch string) error {
	c.lock.Acquire()
	defer c.lock.Release()
	return c.repo.MergeBranch(branch, baseBranch)
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
			c.repo.AbortRebase(t.WorktreePath) // best-effort
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

// waitForCleanTarget polls the repo root until it has no pending changes.
// While waiting it transitions the task into the merge-blocked state via the
// injected StatusSetter so the UI can show why work is stalled. Runs outside
// any lock — the goal is to let other tasks finish their merges.
func (c *Coordinator) waitForCleanTarget(ctx context.Context, t *task.Task) error {
	dirty, err := c.repo.HasChanges()
	if err != nil {
		return fmt.Errorf("failed to check target branch: %w", err)
	}
	if !dirty {
		return nil
	}

	log.Printf("Task #%d: target branch has pending changes, entering merge-blocked state", t.ID)
	if c.setStatus != nil {
		if err := c.setStatus(t.ID, task.StatusMergeBlocked); err != nil {
			log.Printf("Warning: failed to set merge-blocked status for task #%d: %v", t.ID, err)
		}
	}

	ticker := time.NewTicker(c.cfg.BlockedPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			dirty, err := c.repo.HasChanges()
			if err != nil {
				log.Printf("Task #%d: failed to check target branch: %v", t.ID, err)
				continue
			}
			if !dirty {
				log.Printf("Task #%d: target branch is clean, proceeding with merge", t.ID)
				return nil
			}
			log.Printf("Task #%d: target branch still has pending changes, retrying", t.ID)
		}
	}
}

// recordBaseCommit appends the new HEAD of the base repo (the fast-forward
// target, or the merge commit when fast-forward wasn't possible) to the
// task's commit history.
func (c *Coordinator) recordBaseCommit(t *task.Task) {
	if c.recordCommit == nil {
		return
	}
	hash, err := c.repo.GetLastCommitHash(c.repo.Root())
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
