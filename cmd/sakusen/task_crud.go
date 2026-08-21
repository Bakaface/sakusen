package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Bakaface/sakusen/internal/action"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/spf13/cobra"
)

var createCmd = &cobra.Command{
	Use:   "create <input>",
	Short: "Create a new task",
	Long: `Create a new task with the given input.

The input can be provided as a positional argument or via stdin.
The daemon must be running.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var input string
		if len(args) == 1 {
			input = args[0]
		} else {
			// Read from stdin if no argument provided
			stat, err := os.Stdin.Stat()
			if err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
				scanner := bufio.NewScanner(os.Stdin)
				var lines []string
				for scanner.Scan() {
					lines = append(lines, scanner.Text())
				}
				input = strings.Join(lines, "\n")
			}
		}

		input = strings.TrimSpace(input)

		priority, _ := cmd.Flags().GetString("priority")
		branch, _ := cmd.Flags().GetString("branch")
		workflow, _ := cmd.Flags().GetString("workflow")
		title, _ := cmd.Flags().GetString("title")
		noWorktree, _ := cmd.Flags().GetBool("no-worktree")
		// Only treat the worktree as an explicit choice when --no-worktree was
		// actually passed. Otherwise leave it unset (nil) so a workflow's worktree
		// pin (or the project default) decides — sending an unsolicited explicit
		// value would clobber a worktree:false pin.
		var worktree *bool
		if cmd.Flags().Changed("no-worktree") {
			w := !noWorktree
			worktree = &w
		}
		target, _ := cmd.Flags().GetString("target")
		checkout, _ := cmd.Flags().GetString("checkout")
		track, _ := cmd.Flags().GetString("track")

		// Early priority validation gives the user immediate feedback before
		// we try to load any project context. The action's own Validate() will
		// repeat this check; both share task.IsValidPriority.
		if priority != "" && !task.IsValidPriority(priority) {
			return fmt.Errorf("invalid priority %q (allowed: low, medium, high, urgent)", priority)
		}

		if input == "" && checkout == "" && !workflowAllowsEmptyInput(cfg, workflow) {
			return fmt.Errorf("input is required (provide as argument or via stdin)")
		}

		createArgs := action.CreateArgs{
			Title:       title,
			Input:       input,
			Priority:    priority,
			Branch:      branch,
			Workflow:    workflow,
			Target:      target,
			Checkout:    checkout,
			Worktree:    worktree,
			Track:       track,
			ProjectPath: cfg.ProjectDir,
		}

		return runAction(cmd, func(actx action.Ctx) (action.Result, error) {
			return action.RunCreate(actx, createArgs)
		})
	},
}

var editCmd = &cobra.Command{
	Use:   "edit <task_id>",
	Short: "Edit a task's fields",
	Long: `Edit a task's title, input, context, or priority.

At least one field flag must be provided.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeTaskIDs(),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid task ID: %s", args[0])
		}

		// Cobra's flag testing API trips Changed=true even when Set sets the
		// flag to its zero value, so we keep a value-level "no flags provided"
		// guard here to preserve the CLI contract that an empty edit is a
		// usage error rather than a no-op patch.
		title, _ := cmd.Flags().GetString("title")
		input, _ := cmd.Flags().GetString("input")
		ctxStr, _ := cmd.Flags().GetString("context")
		priority, _ := cmd.Flags().GetString("priority")

		if title == "" && input == "" && ctxStr == "" && priority == "" {
			return fmt.Errorf("at least one field flag is required (--title, --input, --context, --priority)")
		}

		editArgs := action.EditArgs{ID: taskID}
		if title != "" {
			editArgs.Title = &title
		}
		if input != "" {
			editArgs.Input = &input
		}
		if ctxStr != "" {
			editArgs.Context = &ctxStr
		}
		if priority != "" {
			editArgs.Priority = &priority
		}

		if err := editArgs.Validate(); err != nil {
			return err
		}

		return runAction(cmd, func(actx action.Ctx) (action.Result, error) {
			return action.RunEdit(actx, editArgs)
		})
	},
}

var deleteCmd = &cobra.Command{
	Use:               "delete <task_id>",
	Short:             "Delete a task",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeTaskIDs(),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid task ID: %s", args[0])
		}

		yes, _ := cmd.Flags().GetBool("yes")
		if !yes {
			fmt.Printf("Delete task #%d? This will stop any running agent, remove the worktree, and delete the branch. [y/N] ", taskID)
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer != "y" && answer != "yes" {
				fmt.Println("Cancelled")
				return nil
			}
		}

		return runAction(cmd, func(actx action.Ctx) (action.Result, error) {
			return action.RunDelete(actx, action.DeleteArgs{ID: taskID})
		})
	},
}
