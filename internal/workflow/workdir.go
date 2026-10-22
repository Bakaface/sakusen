package workflow

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Bakaface/sakusen/internal/task"
)

// AgentWorkDir returns the directory a task's agents and tmux sessions start
// in: the checkout root (t.WorktreePath) joined with the task's workdir.
// sakusen's own files (.sakusen/, logs, sentinels) stay at the checkout root.
func AgentWorkDir(t *task.Task) string {
	if t.Workdir == "" {
		return t.WorktreePath
	}
	return filepath.Join(t.WorktreePath, t.Workdir)
}

// ResolveAgentWorkDir is AgentWorkDir plus a check that a configured workdir
// exists as a directory — an agent is never silently started at the root.
func ResolveAgentWorkDir(t *task.Task) (string, error) {
	dir := AgentWorkDir(t)
	if t.Workdir == "" {
		return dir, nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("workdir %q: %s does not exist", t.Workdir, dir)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workdir %q: %s is not a directory", t.Workdir, dir)
	}
	return dir, nil
}
