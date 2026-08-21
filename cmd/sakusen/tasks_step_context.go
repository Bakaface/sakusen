package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/spf13/cobra"
)

// stepContextCmd dumps a task's saved per-step context. It is designed for
// shell piping: with --step, output is the raw stored text with no headers,
// no decoration, and no extra trailing newline — so `> file.md` produces a
// byte-for-byte copy. With --all, the output is JSON ({step_name: context})
// so a downstream tool can consume it deterministically.
var stepContextCmd = &cobra.Command{
	Use:   "step-context <task_id>",
	Short: "Print a task's saved step context to stdout",
	Long: `Print a workflow step's saved context for a task, suitable for piping
to a file (e.g. ` + "`sakusen step-context 42 --step planning > /tmp/PRD.md`" + `).

With --step, the raw stored context is written to stdout exactly as
captured — no headers, no decoration, and no extra trailing newline.

With --all, every completed step's context is written as a single JSON
object ({step_name: context}) for machine consumption.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeTaskIDs(),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid task ID: %s", args[0])
		}

		stepName, _ := cmd.Flags().GetString("step")
		all, _ := cmd.Flags().GetBool("all")

		if err := validateStepContextFlags(stepName, all); err != nil {
			return err
		}

		c := client.New(cfg)
		if err := c.Connect(); err != nil {
			return fmt.Errorf("failed to connect to daemon: %w", err)
		}
		defer c.Close()

		// GetTask first so a missing task surfaces a clear error rather than
		// the silent "empty map" we'd get from GetStepContexts alone.
		if _, err := c.GetTask(taskID); err != nil {
			return fmt.Errorf("task #%d not found: %w", taskID, err)
		}

		steps, err := c.GetStepContexts(taskID)
		if err != nil {
			return fmt.Errorf("failed to get step contexts: %w", err)
		}

		return writeStepContext(cmd.OutOrStdout(), steps, stepName, all)
	},
}

// validateStepContextFlags enforces that exactly one of --step / --all is set.
// Pulled out so the unit tests can exercise the argument-parsing rules
// without spinning up a daemon.
func validateStepContextFlags(stepName string, all bool) error {
	if stepName == "" && !all {
		return fmt.Errorf("either --step <name> or --all is required")
	}
	if stepName != "" && all {
		return fmt.Errorf("--step and --all are mutually exclusive")
	}
	return nil
}

// writeStepContext renders the response for both --step and --all modes.
// Kept io.Writer-based so tests can capture output without touching stdout.
func writeStepContext(w io.Writer, steps map[string]string, stepName string, all bool) error {
	if all {
		// Use a plain Encoder (no SetIndent) so the output is compact and
		// easy to pipe into `jq`. We still want a trailing newline because
		// json.Encoder writes one and shells expect it from JSON output.
		enc := json.NewEncoder(w)
		if steps == nil {
			steps = map[string]string{}
		}
		return enc.Encode(steps)
	}

	ctx, ok := steps[stepName]
	if !ok {
		return fmt.Errorf("step %q not found; available steps: %s",
			stepName, formatAvailableSteps(steps))
	}
	// Write the raw bytes — no fmt.Println, no trailing newline added.
	// This is what makes `> file.md` produce a byte-for-byte copy.
	_, err := io.WriteString(w, ctx)
	return err
}

// formatAvailableSteps renders the keys of `steps` as a stable,
// comma-separated list for inclusion in error messages. Returns "(none)"
// when no completed steps exist so the user can tell the difference between
// "wrong name" and "nothing saved yet".
func formatAvailableSteps(steps map[string]string) string {
	if len(steps) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(steps))
	for name := range steps {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
