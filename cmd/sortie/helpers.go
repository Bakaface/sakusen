package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/Bakaface/sortie/internal/config"
	"github.com/Bakaface/sortie/internal/db"
	"github.com/Bakaface/sortie/internal/task"
	"github.com/spf13/cobra"
)

// workflowAllowsEmptyInput returns true when the named workflow can be
// started without an explicit -i/--input. This holds when the workflow
// pins an input (the pin supplies it) or when its first step runs in tmux
// (the user drives the session interactively).
func workflowAllowsEmptyInput(cfg *config.Config, workflowName string) bool {
	if cfg == nil {
		return false
	}
	wf := cfg.GetWorkflow(workflowName)
	if wf == nil {
		return false
	}
	return wf.Input != "" || wf.FirstStepIsTmux()
}

// taskTableRow holds the display fields for a single row in the task list table.
type taskTableRow struct {
	id     int64
	status string
	step   string
	title  string
}

// printTaskTable prints a formatted table of tasks to stdout.
func printTaskTable(rows []taskTableRow) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tSTEP\tTITLE")
	fmt.Fprintln(w, "--\t------\t----\t-----")
	for _, r := range rows {
		fmt.Fprintf(w, "#%d\t%s\t%s\t%s\n", r.id, r.status, r.step, r.title)
	}
	w.Flush()
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func completeTaskIDs(statuses ...task.Status) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		if cfg == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		dbPath := cfg.GetDatabasePath("")
		database, err := db.Open(dbPath)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		defer database.Close()

		tasks, err := database.GetAllTasks()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		statusFilter := make(map[task.Status]bool, len(statuses))
		for _, s := range statuses {
			statusFilter[s] = true
		}

		var completions []string
		for _, t := range tasks {
			if len(statusFilter) > 0 && !statusFilter[t.Status] {
				continue
			}
			title := t.Title
			if title == "" {
				title = truncateStr(t.Input, 40)
			}
			completions = append(completions, fmt.Sprintf("%d\t[%s] %s", t.ID, t.Status, title))
		}

		return completions, cobra.ShellCompDirectiveNoFileComp
	}
}
