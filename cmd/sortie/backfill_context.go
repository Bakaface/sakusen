package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Bakaface/sortie/internal/db"
	"github.com/Bakaface/sortie/internal/task"
	"github.com/Bakaface/sortie/internal/workflow"
	"github.com/spf13/cobra"
)

var (
	backfillContextDryRun   bool
	backfillContextIDs      []int64
	backfillContextProject  string
	backfillContextModel    string
	backfillContextAllowAll bool
)

var backfillContextCmd = &cobra.Command{
	Use:   "backfill-context",
	Short: "Generate context summaries for completed tasks with empty context",
	Long: `Generate context summaries for completed tasks that have empty context.

Targets tasks where status='completed', context is NULL/empty, and the
commits field has at least one recorded commit SHA. For each candidate the
command computes a diff stat against the parent of the first stored commit
and runs ` + "`claude -p`" + ` with the same diff-stat-fallback prompt the
live summarizer uses, then writes the result via UpdateTaskContext.

Tasks whose merge failed (no stored commits) are skipped because there is
no merge artifact to summarize from.`,
	RunE: runBackfillContext,
}

func init() {
	backfillContextCmd.Flags().BoolVar(&backfillContextDryRun, "dry-run", false, "Print candidates and prompts without invoking claude or writing the DB")
	backfillContextCmd.Flags().Int64SliceVar(&backfillContextIDs, "id", nil, "Restrict to these task IDs (repeatable, comma-separated). Default: all matching candidates.")
	backfillContextCmd.Flags().StringVar(&backfillContextProject, "project", "", "Restrict to project at this absolute path (default: cwd's project)")
	backfillContextCmd.Flags().StringVar(&backfillContextModel, "model", "haiku", "Claude model alias to use for summarization")
	backfillContextCmd.Flags().BoolVar(&backfillContextAllowAll, "all-projects", false, "Process candidates across every registered project (overrides --project)")
}

func runBackfillContext(cmd *cobra.Command, args []string) error {
	dbPath := cfg.GetDatabasePath("")
	database, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db %s: %w", dbPath, err)
	}
	defer database.Close()

	projectFilter, err := resolveBackfillProjectFilter(database)
	if err != nil {
		return err
	}

	allTasks, err := database.GetAllTasks()
	if err != nil {
		return fmt.Errorf("list tasks: %w", err)
	}

	idFilter := map[int64]bool{}
	for _, id := range backfillContextIDs {
		idFilter[id] = true
	}

	var candidates []*task.Task
	for _, t := range allTasks {
		if t.Status != task.StatusCompleted {
			continue
		}
		if strings.TrimSpace(t.Context) != "" {
			continue
		}
		if len(t.Commits) == 0 {
			continue
		}
		if len(idFilter) > 0 && !idFilter[t.ID] {
			continue
		}
		if projectFilter != nil && t.ProjectID != projectFilter.ID {
			continue
		}
		candidates = append(candidates, t)
	}

	if len(candidates) == 0 {
		fmt.Println("No backfill candidates found.")
		return nil
	}

	fmt.Printf("Found %d candidate task(s) to backfill.\n", len(candidates))

	projectPathCache := map[int64]string{}
	getProjectPath := func(projectID int64) (string, error) {
		if p, ok := projectPathCache[projectID]; ok {
			return p, nil
		}
		proj, err := database.GetProject(projectID)
		if err != nil {
			return "", err
		}
		projectPathCache[projectID] = proj.Path
		return proj.Path, nil
	}

	ctx := context.Background()
	var successes, failures, skipped int

	for _, t := range candidates {
		projectPath, err := getProjectPath(t.ProjectID)
		if err != nil {
			fmt.Printf("[#%d] skip: lookup project: %v\n", t.ID, err)
			skipped++
			continue
		}

		diffStat, err := computeBackfillDiffStat(projectPath, t.Commits)
		if err != nil {
			fmt.Printf("[#%d] skip: %v\n", t.ID, err)
			skipped++
			continue
		}
		if strings.TrimSpace(diffStat) == "" {
			fmt.Printf("[#%d] skip: empty diff stat\n", t.ID)
			skipped++
			continue
		}

		prompt := workflow.BuildDiffStatSummaryPrompt(t.ID, t.Title, t.Input, diffStat)

		fmt.Printf("[#%d] %s (project=%s, commits=%d, prompt=%d bytes)\n",
			t.ID, t.Title, projectPath, len(t.Commits), len(prompt))

		if backfillContextDryRun {
			fmt.Printf("[#%d] --- prompt preview ---\n%s\n[#%d] --- end preview ---\n",
				t.ID, truncateForPreview(prompt, 800), t.ID)
			continue
		}

		summary, err := runClaudeBackfill(ctx, prompt, projectPath, backfillContextModel)
		if err != nil {
			fmt.Printf("[#%d] FAILED: %v\n", t.ID, err)
			failures++
			continue
		}
		summary = strings.TrimSpace(summary)
		if summary == "" {
			fmt.Printf("[#%d] FAILED: claude returned empty output\n", t.ID)
			failures++
			continue
		}

		if err := database.UpdateTaskContext(t.ID, summary); err != nil {
			fmt.Printf("[#%d] FAILED to write context: %v\n", t.ID, err)
			failures++
			continue
		}
		fmt.Printf("[#%d] wrote %d chars\n", t.ID, len(summary))
		successes++
	}

	if backfillContextDryRun {
		fmt.Printf("\nDry run complete. %d candidate(s) examined, %d skipped.\n", len(candidates), skipped)
		return nil
	}

	fmt.Printf("\nBackfill complete: %d succeeded, %d failed, %d skipped.\n", successes, failures, skipped)
	if failures > 0 {
		return fmt.Errorf("%d task(s) failed to backfill", failures)
	}
	return nil
}

// resolveBackfillProjectFilter returns the project the user wants to filter to,
// or nil for "all projects". An explicit --all-projects overrides --project.
func resolveBackfillProjectFilter(database *db.DB) (*db.Project, error) {
	if backfillContextAllowAll {
		return nil, nil
	}
	if backfillContextProject != "" {
		proj, err := database.GetProjectByPath(backfillContextProject)
		if err != nil {
			return nil, fmt.Errorf("project %q not found in db: %w", backfillContextProject, err)
		}
		return proj, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	proj, err := database.GetProjectByPath(cwd)
	if err != nil {
		return nil, fmt.Errorf("no project registered for cwd %q; pass --project or --all-projects: %w", cwd, err)
	}
	return proj, nil
}

// computeBackfillDiffStat runs `git diff --stat <first>~1..<last>` in repoPath.
// Verifies all SHAs exist locally before invoking diff so a missing commit
// produces a clear skip message rather than a cryptic git failure.
func computeBackfillDiffStat(repoPath string, commits []string) (string, error) {
	if len(commits) == 0 {
		return "", fmt.Errorf("no commits recorded")
	}
	for _, sha := range commits {
		check := exec.Command("git", "-C", repoPath, "cat-file", "-e", sha+"^{commit}")
		if err := check.Run(); err != nil {
			return "", fmt.Errorf("commit %s not found in %s", sha, repoPath)
		}
	}

	first := commits[0]
	last := commits[len(commits)-1]
	rangeSpec := first + "~1.." + last

	out, err := exec.Command("git", "-C", repoPath, "diff", "--stat", rangeSpec).Output()
	if err != nil {
		return "", fmt.Errorf("git diff --stat %s: %w", rangeSpec, err)
	}
	return string(out), nil
}

// runClaudeBackfill invokes the configured claude binary synchronously with
// the prompt on stdin and the project path as cwd, mirroring the live
// summarizer's invocation shape.
func runClaudeBackfill(ctx context.Context, prompt, workDir, model string) (string, error) {
	if model == "" {
		model = "haiku"
	}
	args := []string{"-p", "--output-format", "text", "--model", model}
	args = append(args, cfg.Claude.Args()...)

	cmd := exec.CommandContext(ctx, cfg.Claude.Command, args...)
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(os.Environ(), "SORTIE_PURPOSE=backfill_context")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude failed: %w (stderr: %s)", err, truncateForPreview(stderr.String(), 400))
	}
	return stdout.String(), nil
}

func truncateForPreview(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("... (%d more bytes)", len(s)-max)
}
