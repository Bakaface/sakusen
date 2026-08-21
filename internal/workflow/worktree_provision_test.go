package workflow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/task"
)

// newProvisionTestEngine wires up a real (in-memory) DB-backed Engine plus a
// throwaway git repo to act as repoRoot, mirroring the pattern used by
// TestCleanupMergedWorktreeLogsMessages and friends above. Returns the
// engine and the created task row.
func newProvisionTestEngine(t *testing.T, taskTitle string) (*Engine, *task.Task) {
	t.Helper()
	repoRoot := initFastTrackTestRepo(t)

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	proj, err := database.GetOrCreateProject(repoRoot)
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	tk, err := database.CreateTask(proj.ID, taskTitle, "desc", "slug", "default", "", task.StatusPending, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	tk.Worktree = true

	cfg := &config.Config{Git: config.GitConfig{BranchTemplate: "task-{{task_id}}"}}
	engine := NewEngine(cfg, database, nil, repoRoot)
	return engine, tk
}

// TestEnsureWorktreeCreatesAndPersists verifies the new-branch (non-checkout)
// path: branch resolution, worktree creation, and DB persistence of both.
func TestEnsureWorktreeCreatesAndPersists(t *testing.T) {
	e, tk := newProvisionTestEngine(t, "create task")

	path, provisioned, err := e.EnsureWorktree(tk, false)
	if err != nil {
		t.Fatalf("EnsureWorktree failed: %v", err)
	}
	if !provisioned {
		t.Error("expected provisioned=true on first call")
	}
	if path == "" || path != tk.WorktreePath {
		t.Errorf("expected returned path to equal tk.WorktreePath, got %q vs %q", path, tk.WorktreePath)
	}
	if tk.Branch == "" {
		t.Error("expected branch to be resolved and set on the task")
	}

	refreshed, err := e.database.GetTask(tk.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if refreshed.Branch != tk.Branch {
		t.Errorf("expected persisted branch %q, got %q", tk.Branch, refreshed.Branch)
	}
	if refreshed.WorktreePath != tk.WorktreePath {
		t.Errorf("expected persisted worktree path %q, got %q", tk.WorktreePath, refreshed.WorktreePath)
	}
}

// TestEnsureWorktreeReusesWithoutDiskCheck verifies RunTask's historical
// idempotency semantics: a non-empty WorktreePath is trusted as already
// provisioned even when checkDisk=false and the directory doesn't actually
// exist on disk.
func TestEnsureWorktreeReusesWithoutDiskCheck(t *testing.T) {
	e, tk := newProvisionTestEngine(t, "reuse task")
	tk.Branch = "already-resolved"
	tk.WorktreePath = filepath.Join(t.TempDir(), "does-not-exist")

	path, provisioned, err := e.EnsureWorktree(tk, false)
	if err != nil {
		t.Fatalf("EnsureWorktree failed: %v", err)
	}
	if provisioned {
		t.Error("expected provisioned=false when WorktreePath is already set and checkDisk=false")
	}
	if path != tk.WorktreePath {
		t.Errorf("expected path to be unchanged, got %q", path)
	}
}

// TestEnsureWorktreeRecreatesWhenDiskCheckFindsMissingDir verifies the
// daemon's recreation semantics: checkDisk=true additionally verifies the
// directory exists, and recreates the worktree when it's gone even though
// WorktreePath was persisted from an earlier run.
func TestEnsureWorktreeRecreatesWhenDiskCheckFindsMissingDir(t *testing.T) {
	e, tk := newProvisionTestEngine(t, "recreate task")

	// First provisioning (checkDisk doesn't matter here — nothing exists yet).
	if _, provisioned, err := e.EnsureWorktree(tk, true); err != nil || !provisioned {
		t.Fatalf("initial EnsureWorktree failed: provisioned=%v err=%v", provisioned, err)
	}
	originalPath := tk.WorktreePath

	// Simulate cleanupWorktreeAndBranch having removed the worktree out from
	// under the persisted path (via the real git-worktree-remove path, not a
	// bare os.RemoveAll, so git's own admin state agrees the worktree is
	// gone and a fresh `git worktree add` at the same path is legal).
	if err := e.repo.RemoveWorktree(originalPath); err != nil {
		t.Fatalf("failed to remove worktree: %v", err)
	}

	path, provisioned, err := e.EnsureWorktree(tk, true)
	if err != nil {
		t.Fatalf("EnsureWorktree (recreate) failed: %v", err)
	}
	if !provisioned {
		t.Error("expected provisioned=true when the worktree directory had been removed")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("expected recreated worktree to exist on disk at %q: %v", path, statErr)
	}
}

// TestEnsureWorktreeCheckoutBranchPreservesExistingBranch verifies that
// CheckoutBranch mode does not clobber an already-resolved t.Branch — the
// unification standardized on ensureWorktree's "only set if empty" behavior
// (the two pre-refactor implementations agreed on the end state but RunTask
// redundantly re-wrote the same value on every call).
func TestEnsureWorktreeCheckoutBranchPreservesExistingBranch(t *testing.T) {
	e, tk := newProvisionTestEngine(t, "checkout task")
	// "main" is already checked out at repoRoot itself, so `git worktree add`
	// would reject it — use a separate, not-checked-out-anywhere branch.
	runFastTrackGit(t, e.repoRoot, "branch", "feature-checkout")
	tk.CheckoutBranch = "feature-checkout"
	tk.Branch = "feature-checkout" // already resolved from a prior call

	_, provisioned, err := e.EnsureWorktree(tk, false)
	if err != nil {
		t.Fatalf("EnsureWorktree failed: %v", err)
	}
	if !provisioned {
		t.Error("expected provisioned=true on first call")
	}
	if tk.Branch != "feature-checkout" {
		t.Errorf("expected branch to remain %q, got %q", "feature-checkout", tk.Branch)
	}
}

// TestEnsureWorktreeNonWorktreeTaskIsNoop verifies that non-worktree tasks
// are left entirely untouched — callers own that path themselves.
func TestEnsureWorktreeNonWorktreeTaskIsNoop(t *testing.T) {
	e, tk := newProvisionTestEngine(t, "no-worktree task")
	tk.Worktree = false

	path, provisioned, err := e.EnsureWorktree(tk, false)
	if err != nil {
		t.Fatalf("EnsureWorktree failed: %v", err)
	}
	if provisioned {
		t.Error("expected provisioned=false for a non-worktree task")
	}
	if path != "" {
		t.Errorf("expected empty path for a non-worktree task, got %q", path)
	}
}
