package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/spf13/cobra"
)

// waitForTasksCmd exposes the daemon's MsgWaitForTasks RPC as a CLI command.
// Primary use cases:
//   - tests (the e2e suite invokes it from the stub-agent.sh harness so a
//     fake step can record waits-on edges without speaking MCP)
//   - power users / scripts that want to suspend an already-running parent
//     step on a hand-picked set of children
//
// The MCP create_tasks_and_wait / wait_for_tasks tools remain the recommended
// surface for agents — this CLI is the corresponding human/test-only path.
var waitForTasksCmd = &cobra.Command{
	Use:   "wait-for-tasks <parent_task_id> <child_task_id>...",
	Short: "Record waits-on edges so the parent's current step suspends until each child reaches terminal status",
	Long: `Record task_waits_on edges from <parent_task_id> to each of the supplied
<child_task_id> values. The parent's currently-running step will suspend on
engine return (status becomes "awaiting-children"); the daemon poller resumes
the parent at the same step once every child reaches terminal status.

When --use-env is set, <parent_task_id> is read from $SAKUSEN_TASK_ID instead
of the positional args (useful from inside a workflow step's subprocess).`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		useEnv, _ := cmd.Flags().GetBool("use-env")

		var (
			parentID  int64
			childArgs []string
		)
		if useEnv {
			envID := os.Getenv("SAKUSEN_TASK_ID")
			if envID == "" {
				return fmt.Errorf("--use-env requires SAKUSEN_TASK_ID to be set")
			}
			id, err := strconv.ParseInt(envID, 10, 64)
			if err != nil || id <= 0 {
				return fmt.Errorf("invalid SAKUSEN_TASK_ID=%q", envID)
			}
			parentID = id
			childArgs = args
		} else {
			if len(args) < 2 {
				return fmt.Errorf("requires <parent_task_id> and at least one <child_task_id>")
			}
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid parent task ID: %s", args[0])
			}
			parentID = id
			childArgs = args[1:]
		}

		childIDs := make([]int64, 0, len(childArgs))
		for _, s := range childArgs {
			id, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid child task ID: %s", s)
			}
			childIDs = append(childIDs, id)
		}

		c := client.New(cfg)
		if err := c.Connect(); err != nil {
			return fmt.Errorf("failed to connect to daemon: %w", err)
		}
		defer c.Close()

		children, err := c.WaitForTasks(parentID, childIDs)
		if err != nil {
			return fmt.Errorf("wait-for-tasks: %w", err)
		}

		ids := make([]string, len(children))
		for i, ch := range children {
			ids[i] = strconv.FormatInt(ch.ID, 10)
		}
		if len(children) == 0 {
			fmt.Printf("Parent task #%d: every supplied child was already terminal — no suspension recorded\n", parentID)
			return nil
		}
		fmt.Printf("Parent task #%d will suspend on %d child task(s): %v\n", parentID, len(children), ids)
		return nil
	},
}

func init() {
	waitForTasksCmd.Flags().Bool("use-env", false, "Use $SAKUSEN_TASK_ID as parent_task_id (omit the positional arg)")
	rootCmd.AddCommand(waitForTasksCmd)
}
