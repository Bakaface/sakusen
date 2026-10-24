package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/config"
	"github.com/spf13/cobra"
)

// waitExitTimedOut is the exit code for a wait that ended without a match.
const waitExitTimedOut = 2

var waitCmd = &cobra.Command{
	Use:   "wait <task_id>...",
	Short: "Block until any of the given tasks reaches a target status",
	Long: `Block until any of the given tasks reaches one of the target statuses, then
print a JSON result {matched, tasks, timed_out} to stdout.

Default statuses: completed (the branch landed), failed, merge-failed and
merge-blocked (not terminal, but needs attention). Override with --status,
repeatable. When waiting on several tasks, re-run with the IDs not yet matched.

Meant for scripts and background shell use; it does not need $SAKUSEN_TASK_ID.

Exit codes: 0 = a task matched, 2 = timed out, 1 = error.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		statuses, _ := cmd.Flags().GetStringArray("status")
		timeout, _ := cmd.Flags().GetDuration("timeout")
		code, err := runWait(cmd.Context(), cmd.OutOrStdout(), cfg, args, statuses, timeout)
		if err != nil {
			return err
		}
		if code != 0 {
			os.Exit(code)
		}
		return nil
	},
}

// runWait performs the wait and writes the JSON result to out, returning the
// process exit code for a non-error outcome.
func runWait(ctx context.Context, out io.Writer, cfg *config.Config, args, statusNames []string, timeout time.Duration) (int, error) {
	ids := make([]int64, 0, len(args))
	for _, a := range args {
		id, err := strconv.ParseInt(a, 10, 64)
		if err != nil || id <= 0 {
			return 1, fmt.Errorf("invalid task ID: %s", a)
		}
		ids = append(ids, id)
	}
	statuses, err := client.ParseWaitStatuses(statusNames)
	if err != nil {
		return 1, err
	}
	if timeout < 0 {
		return 1, fmt.Errorf("--timeout must not be negative")
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	c := client.New(cfg)
	if err := c.Connect(); err != nil {
		return 1, fmt.Errorf("failed to connect to daemon: %w", err)
	}
	defer c.Close()

	result, err := c.WaitForTaskStatus(ctx, ids, statuses, 0)
	if err != nil {
		return 1, fmt.Errorf("wait: %w", err)
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return 1, err
	}
	fmt.Fprintln(out, string(data))
	if result.TimedOut {
		return waitExitTimedOut, nil
	}
	return 0, nil
}

func init() {
	waitCmd.Flags().StringArray("status", nil, "Target status (repeatable; default: completed, failed, merge-failed, merge-blocked)")
	waitCmd.Flags().Duration("timeout", 0, "Maximum time to wait, e.g. 30m (0 = wait indefinitely)")
	rootCmd.AddCommand(waitCmd)
}
