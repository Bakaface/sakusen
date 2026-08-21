package main

import (
	"fmt"
	"strconv"

	"github.com/Bakaface/sakusen/internal/action"
	"github.com/Bakaface/sakusen/internal/client"
	"github.com/spf13/cobra"
)

var dependsOnCmd = &cobra.Command{
	Use:   "depends-on",
	Short: "Manage task dependencies (which tasks block which)",
}

var dependsOnAddCmd = &cobra.Command{
	Use:   "add <task_id> <blocked_by_id>",
	Short: "Mark <task_id> as blocked by <blocked_by_id>",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID, blockedBy, err := parseDependsOnArgs(args)
		if err != nil {
			return err
		}
		return runAction(cmd, func(actx action.Ctx) (action.Result, error) {
			return action.RunDependsOn(actx, action.DependsOnArgs{TaskID: taskID, BlockedByID: blockedBy, Direction: "add"})
		})
	},
}

var dependsOnRmCmd = &cobra.Command{
	Use:   "rm <task_id> <blocked_by_id>",
	Short: "Remove the dependency that <blocked_by_id> blocks <task_id>",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID, blockedBy, err := parseDependsOnArgs(args)
		if err != nil {
			return err
		}
		return runAction(cmd, func(actx action.Ctx) (action.Result, error) {
			return action.RunDependsOn(actx, action.DependsOnArgs{TaskID: taskID, BlockedByID: blockedBy, Direction: "remove"})
		})
	},
}

var dependsOnListCmd = &cobra.Command{
	Use:   "list <task_id>",
	Short: "List the tasks that block <task_id>",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid task ID: %s", args[0])
		}

		c := client.New(cfg)
		if err := c.Connect(); err != nil {
			return fmt.Errorf("failed to connect to daemon: %w", err)
		}
		defer c.Close()

		t, err := c.GetTask(taskID)
		if err != nil {
			return fmt.Errorf("failed to get task: %w", err)
		}

		if len(t.BlockedBy) == 0 {
			fmt.Printf("Task #%d has no dependencies\n", taskID)
			return nil
		}

		for _, dep := range t.BlockedBy {
			fmt.Println(dep)
		}
		return nil
	},
}

func parseDependsOnArgs(args []string) (int64, int64, error) {
	taskID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid task ID: %s", args[0])
	}
	blockedBy, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid blocked-by ID: %s", args[1])
	}
	if taskID == blockedBy {
		return 0, 0, fmt.Errorf("a task cannot depend on itself")
	}
	return taskID, blockedBy, nil
}
