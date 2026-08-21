package git

import (
	"path/filepath"
	"strings"
)

// Repo represents a git repository rooted at a known path on disk. Methods
// scoped to the main working tree (merges, branch management, worktree
// lifecycle) are called directly on Repo with no path argument; methods
// scoped to a specific task worktree take that worktree's path explicitly,
// since one Repo can have many worktrees checked out under it at once.
//
// Repo is a thin wrapper — it holds no file handles or cached state beyond
// the root path, so constructing one is cheap and callers are free to build
// a fresh Repo per call site rather than threading a shared instance.
type Repo struct {
	root string
}

// NewRepo constructs a Repo rooted at root. The path is used as-is — callers
// that need the canonical top-level directory of a repository (e.g. when
// root might be a subdirectory or a worktree) should resolve it first via
// GetRepoRoot.
func NewRepo(root string) *Repo {
	return &Repo{root: root}
}

// Root returns the repository root path this Repo was constructed with.
func (r *Repo) Root() string { return r.root }

// worktreePath returns the on-disk path sakusen uses for a worktree checking
// out branchName. This is the single place the worktree layout convention
// (<repoRoot>/.sakusen/worktrees/<dir>) and the branch-name-to-directory-name
// sanitization (slashes aren't valid path segments) are defined — both
// CreateWorktree and CheckoutWorktree resolve through it so the convention
// cannot drift between them.
func (r *Repo) worktreePath(branchName string) string {
	dirName := strings.ReplaceAll(branchName, "/", "-")
	return filepath.Join(r.root, ".sakusen", "worktrees", dirName)
}
