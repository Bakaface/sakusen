package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/spf13/cobra"
)

var publishItemsCmd = &cobra.Command{
	Use:   "publish-items",
	Short: "Publish the items a later fanout step spawns child tasks from",
	Long: `Publish a JSON array of items for a later fanout step (whose from: names
this step) to spawn one child task per item. Each item is an object with
"input" (required) and optional "slug", "title" and "needs".

The step must be running; each call replaces the list it published earlier.
--task and --step default to $SAKUSEN_TASK_ID and $SAKUSEN_STEP, so inside a
running step:

  sakusen publish-items --file items.json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		taskFlag, _ := cmd.Flags().GetInt64("task")
		stepFlag, _ := cmd.Flags().GetString("step")
		file, _ := cmd.Flags().GetString("file")

		taskID, step, err := resolvePublishTarget(taskFlag, stepFlag, os.Getenv)
		if err != nil {
			return err
		}

		in := cmd.InOrStdin()
		if file != "" && file != "-" {
			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			in = f
		}
		items, err := parsePublishItems(in)
		if err != nil {
			return err
		}

		c := client.New(cfg)
		if err := c.Connect(); err != nil {
			return fmt.Errorf("failed to connect to daemon: %w", err)
		}
		defer c.Close()

		stored, err := c.PublishItems(taskID, step, items)
		if err != nil {
			return err
		}
		slugs := make([]string, len(stored))
		for i, it := range stored {
			slugs[i] = it.Slug
		}
		fmt.Fprintf(cmd.OutOrStdout(), "published %d items for step %q of task #%d: %s\n", len(stored), step, taskID, strings.Join(slugs, ", "))
		return nil
	},
}

// resolvePublishTarget applies the $SAKUSEN_TASK_ID / $SAKUSEN_STEP defaults
// to unset --task / --step flags.
func resolvePublishTarget(taskFlag int64, stepFlag string, getenv func(string) string) (int64, string, error) {
	taskID := taskFlag
	if taskID < 0 {
		return 0, "", errors.New("--task must be a positive integer")
	}
	if taskID == 0 {
		env := getenv("SAKUSEN_TASK_ID")
		if env == "" {
			return 0, "", errors.New("--task is required (SAKUSEN_TASK_ID not set)")
		}
		id, err := strconv.ParseInt(env, 10, 64)
		if err != nil || id <= 0 {
			return 0, "", fmt.Errorf("invalid SAKUSEN_TASK_ID=%q", env)
		}
		taskID = id
	}
	step := strings.TrimSpace(stepFlag)
	if step == "" {
		step = strings.TrimSpace(getenv("SAKUSEN_STEP"))
		if step == "" {
			return 0, "", errors.New("--step is required (SAKUSEN_STEP not set)")
		}
	}
	return taskID, step, nil
}

// parsePublishItems decodes a JSON array of items. Validation of the items
// themselves is left to the daemon.
func parsePublishItems(r io.Reader) ([]task.PublishedItem, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(data))
	if !strings.HasPrefix(trimmed, "[") {
		return nil, errors.New("items must be a JSON array of objects")
	}
	var items []task.PublishedItem
	if err := json.Unmarshal([]byte(trimmed), &items); err != nil {
		return nil, fmt.Errorf("invalid items JSON: %w", err)
	}
	return items, nil
}

func init() {
	publishItemsCmd.Flags().Int64("task", 0, "Task publishing the items (default $SAKUSEN_TASK_ID)")
	publishItemsCmd.Flags().String("step", "", "Step publishing the items (default $SAKUSEN_STEP)")
	publishItemsCmd.Flags().StringP("file", "f", "-", "JSON file holding the items array ('-' reads stdin)")
	rootCmd.AddCommand(publishItemsCmd)
}
