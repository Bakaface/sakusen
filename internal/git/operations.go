package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Commit stages and commits all pending changes in workDir. workDir is
// explicit rather than implicit on Repo because it may be a task worktree or
// (in non-worktree mode) the repo root itself — the caller decides which
// working directory is being committed. No-op (returns nil) if there is
// nothing to commit.
func (r *Repo) Commit(workDir, message string) error {
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = workDir

	var stderr bytes.Buffer
	addCmd.Stderr = &stderr

	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w (stderr: %s)", err, stderr.String())
	}

	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = workDir

	var stdout bytes.Buffer
	statusCmd.Stdout = &stdout

	if err := statusCmd.Run(); err != nil {
		return fmt.Errorf("git status failed: %w", err)
	}

	if strings.TrimSpace(stdout.String()) == "" {
		return nil
	}

	commitCmd := exec.Command("git", "commit", "-m", message)
	commitCmd.Dir = workDir
	stderr.Reset()
	commitCmd.Stderr = &stderr

	if err := commitCmd.Run(); err != nil {
		return fmt.Errorf("git commit failed: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// GetCurrentBranch returns the name of the branch currently checked out at
// dir. dir may be the repo root or a task worktree — callers decide which
// checkout they're asking about (mirrors GetLastCommitHash).
func (r *Repo) GetCurrentBranch(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to get current branch: %w (stderr: %s)", err, stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

// HasChanges reports whether the repo root's working tree has any
// uncommitted changes (staged or unstaged).
func (r *Repo) HasChanges() (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = r.root

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}

	return strings.TrimSpace(stdout.String()) != "", nil
}

// MergeBranch fast-forwards the repo root's checked-out branch to the tip of
// branch. Fails if a fast-forward isn't possible — callers are expected to
// rebase the task branch onto the base first (via RebaseInto) and retry.
// Linear history is the design: no merge commits are ever produced here, so
// the base branch's log is the task branches' commits in order.
//
// The root is the user's main checkout, so this never checks out a branch or
// discards changes: the caller must ensure the root is clean and on the base
// branch. If not, git refuses the fast-forward rather than clobbering edits.
func (r *Repo) MergeBranch(branch string) error {
	mergeCmd := exec.Command("git", "merge", "--ff-only", branch)
	mergeCmd.Dir = r.root

	var stderr bytes.Buffer
	mergeCmd.Stderr = &stderr

	if err := mergeCmd.Run(); err != nil {
		return fmt.Errorf("git merge --ff-only failed: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// RebaseBranch rebases a branch onto the target branch using the worktree.
// This updates the branch so it's based on the latest target, reducing merge
// conflicts. Unlike RebaseInto, it aborts automatically on conflict.
func (r *Repo) RebaseBranch(worktreePath, baseBranch string) error {
	cmd := exec.Command("git", "rebase", baseBranch)
	cmd.Dir = worktreePath

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Abort the failed rebase to restore clean state
		abortCmd := exec.Command("git", "rebase", "--abort")
		abortCmd.Dir = worktreePath
		abortCmd.Run() // best-effort
		return fmt.Errorf("git rebase %s failed: %w (stderr: %s)", baseBranch, err, stderr.String())
	}

	return nil
}

// ListLocalBranches returns a sorted list of local branch names in the repo,
// excluding the currently checked-out branch.
func (r *Repo) ListLocalBranches() ([]string, error) {
	currentBranch, err := r.GetCurrentBranch(r.root)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("git", "branch", "--list", "--format=%(refname:short)")
	cmd.Dir = r.root

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git branch --list failed: %w (stderr: %s)", err, stderr.String())
	}

	var branches []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && line != currentBranch {
			branches = append(branches, line)
		}
	}

	sort.Strings(branches)
	return branches, nil
}

// DeleteBranch deletes a local branch (fails if not fully merged).
func (r *Repo) DeleteBranch(branch string) error {
	cmd := exec.Command("git", "branch", "-d", branch)
	cmd.Dir = r.root

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git branch -d failed: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// ForceDeleteBranch deletes a local branch regardless of merge status.
func (r *Repo) ForceDeleteBranch(branch string) error {
	cmd := exec.Command("git", "branch", "-D", branch)
	cmd.Dir = r.root

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git branch -D failed: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// HasMeaningfulChanges checks whether workDir has any changes (committed or
// uncommitted) beyond the given exclude list. It checks both:
// - Committed changes vs the base (git diff HEAD --name-only)
// - Uncommitted changes (git status --porcelain)
func (r *Repo) HasMeaningfulChanges(workDir string, excludeFiles []string) (bool, error) {
	excludeSet := make(map[string]bool, len(excludeFiles))
	for _, f := range excludeFiles {
		excludeSet[f] = true
	}

	// Check uncommitted changes
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = workDir
	var statusOut bytes.Buffer
	statusCmd.Stdout = &statusOut
	if err := statusCmd.Run(); err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	for _, line := range strings.Split(statusOut.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Status output format: "XY filename" — extract filename (last field)
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		filename := parts[len(parts)-1]
		if !excludeSet[filename] {
			return true, nil
		}
	}

	// Check committed changes on this branch vs the merge base
	// Use diff against the first parent to see what this branch added
	diffCmd := exec.Command("git", "diff", "HEAD~1", "--name-only")
	diffCmd.Dir = workDir
	var diffOut, diffErr bytes.Buffer
	diffCmd.Stdout = &diffOut
	diffCmd.Stderr = &diffErr
	if err := diffCmd.Run(); err != nil {
		// If HEAD~1 doesn't exist (first commit), that's fine — check log instead
		logCmd := exec.Command("git", "log", "--oneline", "-1")
		logCmd.Dir = workDir
		var logOut bytes.Buffer
		logCmd.Stdout = &logOut
		if logErr := logCmd.Run(); logErr != nil {
			return false, nil
		}
		// If there's at least one commit, consider it has changes
		return strings.TrimSpace(logOut.String()) != "", nil
	}
	for _, line := range strings.Split(diffOut.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !excludeSet[line] {
			return true, nil
		}
	}

	return false, nil
}

// DiffStat returns the --stat output for all commits on workDir's current
// branch relative to the given base branch. Returns empty string if no diff
// is available.
func (r *Repo) DiffStat(workDir, baseBranch string) (string, error) {
	// Find the merge base to diff against
	mergeBaseCmd := exec.Command("git", "merge-base", baseBranch, "HEAD")
	mergeBaseCmd.Dir = workDir
	var mbOut, mbErr bytes.Buffer
	mergeBaseCmd.Stdout = &mbOut
	mergeBaseCmd.Stderr = &mbErr
	if err := mergeBaseCmd.Run(); err != nil {
		// Fallback: try diffing against HEAD~1
		diffCmd := exec.Command("git", "diff", "--stat", "HEAD~1")
		diffCmd.Dir = workDir
		var diffOut bytes.Buffer
		diffCmd.Stdout = &diffOut
		if diffErr := diffCmd.Run(); diffErr != nil {
			return "", nil
		}
		return strings.TrimSpace(diffOut.String()), nil
	}

	mergeBase := strings.TrimSpace(mbOut.String())
	diffCmd := exec.Command("git", "diff", "--stat", mergeBase)
	diffCmd.Dir = workDir
	var diffOut, diffErr2 bytes.Buffer
	diffCmd.Stdout = &diffOut
	diffCmd.Stderr = &diffErr2
	if err := diffCmd.Run(); err != nil {
		return "", fmt.Errorf("git diff --stat failed: %w", err)
	}
	return strings.TrimSpace(diffOut.String()), nil
}

// RebaseInto rebases worktreePath's current branch onto baseBranch. Unlike
// RebaseBranch, it does NOT auto-abort on conflicts — on conflict it returns
// an error but leaves the rebase in progress so the caller can inspect
// conflicted files via GetConflictedFiles, stage resolutions, and call
// ContinueRebase. Callers are responsible for invoking AbortRebase on
// unrecoverable failures.
func (r *Repo) RebaseInto(worktreePath, baseBranch string) error {
	cmd := exec.Command("git", "rebase", baseBranch)
	cmd.Dir = worktreePath

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git rebase %s failed: %w (stderr: %s)", baseBranch, err, stderr.String())
	}

	return nil
}

// GetConflictedFiles returns the list of files with unresolved merge or
// rebase conflicts in workDir. Returns an empty slice if there are no
// conflicts.
func (r *Repo) GetConflictedFiles(workDir string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", "--diff-filter=U")
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff --diff-filter=U failed: %w (stderr: %s)", err, stderr.String())
	}

	var files []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// ContinueRebase stages everything in worktreePath and resumes an
// in-progress rebase, preserving the original commit message. Should be
// called after resolving conflicts from a RebaseInto call.
func (r *Repo) ContinueRebase(worktreePath string) error {
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = worktreePath

	var stderr bytes.Buffer
	addCmd.Stderr = &stderr

	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w (stderr: %s)", err, stderr.String())
	}

	cmd := exec.Command("git", "rebase", "--continue")
	cmd.Dir = worktreePath
	// Prevent git from launching an editor for the commit message; reuse
	// the original message as-is.
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true")
	stderr.Reset()
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git rebase --continue failed: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// AbortRebase aborts an in-progress rebase in worktreePath to restore clean
// state.
func (r *Repo) AbortRebase(worktreePath string) error {
	cmd := exec.Command("git", "rebase", "--abort")
	cmd.Dir = worktreePath

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git rebase --abort failed: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// GetLastCommitHash returns the SHA of the most recent commit on the current
// branch checked out at dir. dir may be the repo root or a task worktree —
// callers decide which HEAD they're asking about.
func (r *Repo) GetLastCommitHash(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get last commit hash: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// RevertCommits performs git revert for each commit hash in reverse order
// (newest first) at the repo root.
func (r *Repo) RevertCommits(commits []string) error {
	// Revert in reverse order (newest first) to avoid conflicts
	for i := len(commits) - 1; i >= 0; i-- {
		cmd := exec.Command("git", "revert", "--no-edit", commits[i])
		cmd.Dir = r.root
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to revert commit %s: %w\n%s", commits[i], err, string(out))
		}
	}
	return nil
}

// GetLastCommitMessage returns the subject+body of the most recent commit at
// workDir.
func (r *Repo) GetLastCommitMessage(workDir string) (string, error) {
	cmd := exec.Command("git", "log", "-1", "--pretty=%B")
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to get last commit: %w (stderr: %s)", err, stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

// conventionalCommitRe matches conventional commit subject lines like "feat: ...", "fix(scope): ...", "feat!: ..."
var conventionalCommitRe = regexp.MustCompile(`^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([^)]+\))?!?:\s`)

// titlePrefixes maps common leading verbs in task titles to conventional commit types.
var titlePrefixes = []struct {
	prefix string
	ccType string
}{
	{"fix ", "fix"},
	{"bugfix ", "fix"},
	{"repair ", "fix"},
	{"refactor ", "refactor"},
	{"rework ", "refactor"},
	{"restructure ", "refactor"},
	{"test ", "test"},
	{"document ", "docs"},
	{"style ", "style"},
	{"optimize ", "perf"},
	{"revert ", "revert"},
}

// ConventionalCommitFromTitle converts a freeform task title into a conventional
// commit message. If the title already is a conventional commit, it's returned as-is.
// Otherwise the leading verb is used to infer the type (defaulting to "feat").
//
// This is a pure string transformation — it doesn't touch any repository, so
// it stays a free function rather than a Repo method.
func ConventionalCommitFromTitle(title string) string {
	if conventionalCommitRe.MatchString(title) {
		return title
	}

	lower := strings.ToLower(title)
	for _, p := range titlePrefixes {
		if strings.HasPrefix(lower, p.prefix) {
			// Strip the verb prefix and lowercase first char of the remainder
			desc := title[len(p.prefix):]
			if desc == "" {
				desc = title
			}
			return p.ccType + ": " + lowercaseFirst(desc)
		}
	}

	return "feat: " + lowercaseFirst(title)
}

func lowercaseFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToLower(r)) + s[size:]
}
