package mcp

import (
	"fmt"
	"os"
	"path/filepath"

	gitpkg "github.com/Bakaface/sakusen/internal/git"
)

// resolveProjectPath returns an absolute repo root for the project the caller
// is targeting. If explicit is non-empty it's normalized to an absolute path
// (the daemon does its own GetOrCreateProject from there). Otherwise the
// caller's cwd is walked up via `git rev-parse --show-toplevel` — the same
// mechanism the TUI uses, ensuring tasks land on the same project row.
func resolveProjectPath(explicit string) (string, error) {
	if explicit != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", fmt.Errorf("invalid project_path: %w", err)
		}
		return abs, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to determine current directory: %w", err)
	}

	root, err := gitpkg.GetRepoRoot(cwd)
	if err != nil {
		return "", fmt.Errorf("project_path not provided and cwd %q is not inside a git repository: %w", cwd, err)
	}
	return root, nil
}
