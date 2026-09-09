package mcp

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Bakaface/sakusen/internal/config"
)

// resolveProjectPath returns an absolute root for the project the caller is
// targeting. If explicit is non-empty it's normalized to an absolute path (the
// daemon does its own GetOrCreateProject from there). Otherwise the caller's
// cwd is walked up to the nearest ancestor holding a .sakusen.yml — bounded by
// the git toplevel, which is the fallback when no marker is found — the same
// resolution the TUI and the CLI use, ensuring tasks land on the same project
// row.
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

	root, kind, err := config.FindProjectRoot(cwd)
	if err != nil {
		return "", fmt.Errorf("failed to resolve project root from cwd %q: %w", cwd, err)
	}
	if kind == config.ProjectRootNone {
		return "", fmt.Errorf("project_path not provided and cwd %q is neither inside a git repository nor a directory containing .sakusen.yml", cwd)
	}
	return root, nil
}
