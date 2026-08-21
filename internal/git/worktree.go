package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const WorktreePrefix = "sakusen-task-"

type Worktree struct {
	Path     string
	Branch   string
	RepoRoot string
}

// CreateWorktree creates a new worktree (and branch, unless it already
// exists) under the repo's .sakusen/worktrees directory. If branchName is
// empty, a name is derived from taskID. If the branch already exists, this
// falls back to adding a worktree that checks it out rather than creating it.
func (r *Repo) CreateWorktree(taskID int64, baseBranch, branchName string) (*Worktree, error) {
	if branchName == "" {
		branchName = fmt.Sprintf("%s%d", WorktreePrefix, taskID)
	}
	worktreePath := r.worktreePath(branchName)

	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create worktree directory: %w", err)
	}

	if _, err := os.Stat(worktreePath); err == nil {
		if err := r.RemoveWorktree(worktreePath); err != nil {
			return nil, fmt.Errorf("failed to remove existing worktree: %w", err)
		}
	}

	if baseBranch == "" {
		baseBranch = r.GetDefaultBranch()
	}

	args := []string{"worktree", "add", "-b", branchName, worktreePath, baseBranch}
	cmd := exec.Command("git", args...)
	cmd.Dir = r.root

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		classified := classifyGitErr(err, stderr.String())
		if !errors.Is(classified, ErrWorktreeExists) {
			if classified != nil {
				return nil, classified
			}
			return nil, fmt.Errorf("failed to create worktree: %w (stderr: %s)", err, stderr.String())
		}
		// Branch/worktree already exists — fall back to checking it out.
		args = []string{"worktree", "add", worktreePath, branchName}
		cmd = exec.Command("git", args...)
		cmd.Dir = r.root
		stderr.Reset()
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("%w: failed to create worktree: %v (stderr: %s)", ErrWorktreeExists, err, stderr.String())
		}
	}

	return &Worktree{
		Path:     worktreePath,
		Branch:   branchName,
		RepoRoot: r.root,
	}, nil
}

// BranchExists reports whether branchName exists as a local branch.
// Internal-only helper used by CheckoutWorktree's fetch-if-missing fallback.
func (r *Repo) BranchExists(branchName string) bool {
	cmd := exec.Command("git", "show-ref", "--verify", "refs/heads/"+branchName)
	cmd.Dir = r.root
	return cmd.Run() == nil
}

// FetchAndTrackBranch fetches branchName from origin and creates a local
// tracking branch if one doesn't already exist. Internal-only helper used by
// CheckoutWorktree.
func (r *Repo) FetchAndTrackBranch(branchName string) error {
	cmd := exec.Command("git", "fetch", "origin", branchName)
	cmd.Dir = r.root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to fetch branch %s: %w (stderr: %s)", branchName, err, stderr.String())
	}

	if !r.BranchExists(branchName) {
		cmd = exec.Command("git", "branch", "--track", branchName, "origin/"+branchName)
		cmd.Dir = r.root
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to create tracking branch %s: %w (stderr: %s)", branchName, err, stderr.String())
		}
	}

	return nil
}

// CheckoutWorktree adds a worktree that checks out an existing branch,
// fetching it from origin first if it isn't available locally.
func (r *Repo) CheckoutWorktree(taskID int64, branchName string) (*Worktree, error) {
	if !r.BranchExists(branchName) {
		if err := r.FetchAndTrackBranch(branchName); err != nil {
			return nil, fmt.Errorf("branch %q not found locally or on remote: %w", branchName, err)
		}
	}

	worktreePath := r.worktreePath(branchName)

	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create worktree directory: %w", err)
	}

	if _, err := os.Stat(worktreePath); err == nil {
		if err := r.RemoveWorktree(worktreePath); err != nil {
			return nil, fmt.Errorf("failed to remove existing worktree: %w", err)
		}
	}

	args := []string{"worktree", "add", worktreePath, branchName}
	cmd := exec.Command("git", args...)
	cmd.Dir = r.root

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to create worktree for branch %s: %w (stderr: %s)", branchName, err, stderr.String())
	}

	return &Worktree{
		Path:     worktreePath,
		Branch:   branchName,
		RepoRoot: r.root,
	}, nil
}

// RemoveWorktree removes the worktree at worktreePath. Tolerates the case
// where the path is already not a registered worktree (ErrNotAWorktree).
func (r *Repo) RemoveWorktree(worktreePath string) error {
	cmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
	cmd.Dir = r.root

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		classified := classifyGitErr(err, stderr.String())
		if !errors.Is(classified, ErrNotAWorktree) {
			if classified != nil {
				return classified
			}
			return fmt.Errorf("failed to remove worktree: %w (stderr: %s)", err, stderr.String())
		}
	}

	if _, err := os.Stat(worktreePath); err == nil {
		os.RemoveAll(worktreePath)
	}

	return nil
}

// ListWorktrees returns the paths of every sakusen-managed worktree
// (identified by the WorktreePrefix / "sakusen-" naming convention)
// registered against the repo root.
func (r *Repo) ListWorktrees() ([]string, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = r.root

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w (stderr: %s)", err, stderr.String())
	}

	var worktrees []string
	lines := strings.Split(stdout.String(), "\n")

	for _, line := range lines {
		if strings.HasPrefix(line, "worktree ") {
			path := strings.TrimPrefix(line, "worktree ")
			if strings.Contains(path, WorktreePrefix) || strings.Contains(path, "sakusen-") {
				worktrees = append(worktrees, path)
			}
		}
	}

	return worktrees, nil
}

// CleanupWorktrees prunes stale worktree administrative files (git worktree
// prune).
func (r *Repo) CleanupWorktrees() error {
	cmd := exec.Command("git", "worktree", "prune")
	cmd.Dir = r.root

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to prune worktrees: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// GetDefaultBranch returns the repo's default branch: it tries the
// origin/HEAD symbolic ref first, then falls back to main/master, then HEAD.
func (r *Repo) GetDefaultBranch() string {
	cmd := exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = r.root

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err == nil {
		ref := strings.TrimSpace(stdout.String())
		parts := strings.Split(ref, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}

	for _, branch := range []string{"main", "master"} {
		cmd := exec.Command("git", "rev-parse", "--verify", branch)
		cmd.Dir = r.root
		if cmd.Run() == nil {
			return branch
		}
	}

	return "HEAD"
}

// IsGitRepo reports whether path is inside a git repository. This is a
// repo-independent utility (it's how callers discover whether a Repo can be
// constructed at all), so it stays a free function rather than a method.
func IsGitRepo(path string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = path

	return cmd.Run() == nil
}

// GetRepoRoot resolves the top-level directory of the git repository
// containing path. Repo-independent (it's how callers discover the root to
// construct a Repo with), so it stays a free function rather than a method.
func GetRepoRoot(path string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = path

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("not a git repository: %w (stderr: %s)", err, stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

// DetachWorktreeHead detaches HEAD in worktreePath (checkout --detach). This
// is a pure worktree-path operation independent of any particular Repo (it
// never touches a repo root), so — like IsGitRepo/GetRepoRoot — it stays a
// free function rather than a Repo method; callers that only have a
// worktree path in hand (and no resolved Repo) can call it directly.
func DetachWorktreeHead(worktreePath string) error {
	cmd := exec.Command("git", "-C", worktreePath, "checkout", "--detach")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to detach HEAD in worktree %s: %w (stderr: %s)", worktreePath, err, stderr.String())
	}
	return nil
}

// ReattachWorktreeBranch checks out branch in worktreePath, reattaching HEAD
// to it after a prior DetachWorktreeHead. Free function for the same reason
// as DetachWorktreeHead.
func ReattachWorktreeBranch(worktreePath, branch string) error {
	cmd := exec.Command("git", "-C", worktreePath, "checkout", branch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to checkout branch %s in worktree %s: %w (stderr: %s)", branch, worktreePath, err, stderr.String())
	}
	return nil
}

// CheckoutBranch checks out branch at the repo root.
func (r *Repo) CheckoutBranch(branch string) error {
	cmd := exec.Command("git", "-C", r.root, "checkout", branch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to checkout branch %s in %s: %w (stderr: %s)", branch, r.root, err, stderr.String())
	}
	return nil
}

// IsWorktreeDetached reports whether worktreePath currently has a detached
// HEAD. Free function for the same reason as DetachWorktreeHead.
func IsWorktreeDetached(worktreePath string) bool {
	cmd := exec.Command("git", "-C", worktreePath, "symbolic-ref", "HEAD")
	return cmd.Run() != nil
}
