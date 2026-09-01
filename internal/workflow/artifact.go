package workflow

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// LogsDir returns the path to the logs directory in a worktree.
func LogsDir(worktreePath string) string {
	return filepath.Join(worktreePath, ".sakusen", "logs")
}

// EnsureWorkDirs creates the .sakusen/logs directory in a worktree.
func EnsureWorkDirs(worktreePath string) error {
	return os.MkdirAll(LogsDir(worktreePath), 0755)
}

// ProjectLogsDir returns the path to the logs directory for a task in the project data dir.
func ProjectLogsDir(dataDir string, taskID int64) string {
	return filepath.Join(dataDir, "logs", fmt.Sprintf("%d", taskID))
}

// ProjectLogPath returns the path to the unified task log file in the project data dir.
// All steps and the finalization phase append into this single file so the log preserves
// chronological order across step boundaries.
func ProjectLogPath(dataDir string, taskID int64) string {
	return filepath.Join(ProjectLogsDir(dataDir, taskID), "task.log")
}

// appendTaskLog appends one timestamped line to the unified task log, matching
// the format runHeadlessAgent and FinalizeTask write. Used for engine-level
// events that belong in the run record a user actually reads rather than only
// in the daemon log. Best-effort: failures are logged and swallowed, never
// propagated into task execution.
func (e *Engine) appendTaskLog(taskID int64, format string, args ...any) {
	if err := os.MkdirAll(ProjectLogsDir(e.dataDir, taskID), 0755); err != nil {
		log.Printf("Warning: failed to create log dir for task #%d: %v", taskID, err)
		return
	}
	f, err := os.OpenFile(ProjectLogPath(e.dataDir, taskID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("Warning: failed to open task log for task #%d: %v", taskID, err)
		return
	}
	defer f.Close()
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	if _, err := f.WriteString(line); err != nil {
		log.Printf("Warning: failed to append to task log for task #%d: %v", taskID, err)
	}
}

// ImagesDir returns the path to the images directory in a worktree.
func ImagesDir(worktreePath string) string {
	return filepath.Join(worktreePath, ".sakusen", "images")
}

// CopyImagesToWorktree copies the given image files into .sakusen/images/ in the worktree.
// Returns the list of worktree-relative paths for the copied images.
func CopyImagesToWorktree(worktreePath string, imagePaths []string) ([]string, error) {
	if len(imagePaths) == 0 {
		return nil, nil
	}

	imagesDir := ImagesDir(worktreePath)
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create images dir: %w", err)
	}

	var relativePaths []string
	for _, src := range imagePaths {
		name := filepath.Base(src)
		dst := filepath.Join(imagesDir, name)

		data, err := os.ReadFile(src)
		if err != nil {
			return nil, fmt.Errorf("failed to read image %s: %w", src, err)
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return nil, fmt.Errorf("failed to write image %s: %w", dst, err)
		}

		relPath, _ := filepath.Rel(worktreePath, dst)
		relativePaths = append(relativePaths, relPath)
	}

	return relativePaths, nil
}
