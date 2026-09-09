package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/spf13/cobra"
)

var routinesCmd = &cobra.Command{
	Use:   "routines",
	Short: "Manage routines (workflow invocation bindings)",
	Long: `List and manage the routines declared under the top-level routines: section
of .sakusen.yml. A routine binds a workflow to a way of invoking it: pins, an
optional cadence, and a name you can run on demand. Routines are read-only here
(edit the yml to add or change them); this command surfaces their schedule,
pause state and run history, and can pause/resume or run one now.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRoutinesList()
	},
}

var routinesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List routines for this project",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRoutinesList()
	},
}

var routinesShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show a routine",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withRoutineClient(func(c *client.Client) error {
			r, err := c.GetRoutine(cfg.ProjectDir, args[0])
			if err != nil {
				return err
			}
			printRoutineDetail(r)
			return nil
		})
	},
}

var routinesPauseCmd = &cobra.Command{
	Use:   "pause <name>",
	Short: "Pause a scheduled routine",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setRoutinePaused(args[0], true) },
}

var routinesResumeCmd = &cobra.Command{
	Use:   "resume <name>",
	Short: "Resume a paused routine",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setRoutinePaused(args[0], false) },
}

var routinesRunsCmd = &cobra.Command{
	Use:   "runs <name>",
	Short: "List tasks a routine has created",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withRoutineClient(func(c *client.Client) error {
			tasks, err := c.ListRoutineRuns(cfg.ProjectDir, args[0])
			if err != nil {
				return err
			}
			if len(tasks) == 0 {
				fmt.Println("No runs yet for this routine.")
				return nil
			}
			rows := make([]taskTableRow, len(tasks))
			for i, t := range tasks {
				title := t.Title
				if title == "" {
					title = truncateStr(t.Input, 50)
				}
				step := t.CurrentStep
				if step == "" {
					step = "-"
				}
				rows[i] = taskTableRow{id: t.ID, status: t.Status, step: step, title: title}
			}
			printTaskTable(rows)
			return nil
		})
	},
}

var routinesRunCmd = &cobra.Command{
	Use:   "run <name> [input]",
	Short: "Run a routine now (does not advance its schedule)",
	Long: `Run a routine now. The optional input argument becomes {{task.input}} for
this run, overriding the routine's own input:. Routines whose workflow needs an
input they do not supply themselves require the argument.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		var input string
		if len(args) == 2 {
			input = args[1]
		}
		return withRoutineClient(func(c *client.Client) error {
			t, err := c.RunRoutine(cfg.ProjectDir, args[0], input)
			if err != nil {
				return err
			}
			fmt.Printf("Ran routine %s → task #%d (%s)\n", args[0], t.ID, t.Title)
			return nil
		})
	},
}

func init() {
	routinesCmd.AddCommand(routinesListCmd)
	routinesCmd.AddCommand(routinesShowCmd)
	routinesCmd.AddCommand(routinesPauseCmd)
	routinesCmd.AddCommand(routinesResumeCmd)
	routinesCmd.AddCommand(routinesRunsCmd)
	routinesCmd.AddCommand(routinesRunCmd)
	rootCmd.AddCommand(routinesCmd)
}

// withRoutineClient connects to the daemon, runs fn, and closes the client.
func withRoutineClient(fn func(*client.Client) error) error {
	c := client.New(cfg)
	if err := c.Connect(); err != nil {
		return fmt.Errorf("daemon not running: %w", err)
	}
	defer c.Close()
	return fn(c)
}

func runRoutinesList() error {
	return withRoutineClient(func(c *client.Client) error {
		routines, err := c.ListRoutines(cfg.ProjectDir)
		if err != nil {
			return fmt.Errorf("failed to list routines: %w", err)
		}
		if len(routines) == 0 {
			fmt.Println("No routines. Add them under the top-level routines: section of .sakusen.yml.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tCADENCE\tWORKFLOW\tNEXT FIRE\tLAST FIRE\tSTATUS")
		fmt.Fprintln(w, "----\t-------\t--------\t---------\t---------\t------")
		for _, r := range routines {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				r.Name, dashIfEmpty(r.Cadence), r.WorkflowRef,
				routineNextFire(r), fmtTime(r.LastFiredAt), routineStatusLabel(r))
		}
		w.Flush()
		return nil
	})
}

func setRoutinePaused(name string, paused bool) error {
	return withRoutineClient(func(c *client.Client) error {
		r, err := c.SetRoutinePaused(cfg.ProjectDir, name, paused)
		if err != nil {
			return err
		}
		fmt.Printf("Routine %s is now %s\n", r.Name, routineStatusLabel(*r))
		return nil
	})
}

func printRoutineDetail(r *daemon.PeriodicInfo) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Name:\t%s\n", r.Name)
	if r.Description != "" {
		fmt.Fprintf(w, "Description:\t%s\n", r.Description)
	}
	fmt.Fprintf(w, "Workflow:\t%s\n", r.WorkflowRef)
	fmt.Fprintf(w, "Cadence:\t%s\n", dashIfEmpty(r.Cadence))
	if r.Input != "" {
		fmt.Fprintf(w, "Input:\t%s\n", truncateStr(r.Input, 60))
	}
	fmt.Fprintf(w, "Requires input:\t%s\n", yesNo(r.RequiresInput))
	priority := r.Priority
	if priority == "" {
		priority = "(project default)"
	}
	fmt.Fprintf(w, "Priority:\t%s\n", priority)
	fmt.Fprintf(w, "Status:\t%s\n", routineStatusLabel(*r))
	fmt.Fprintf(w, "Next fire:\t%s\n", routineNextFire(*r))
	fmt.Fprintf(w, "Last fire:\t%s\n", fmtTime(r.LastFiredAt))
	if r.LastTaskID != nil {
		fmt.Fprintf(w, "Last task:\t#%d\n", *r.LastTaskID)
	}
	w.Flush()
}

// routineNextFire renders the next scheduled fire. On-demand routines carry a
// next_fire_at only because the column is NOT NULL, so it is not shown.
func routineNextFire(r daemon.PeriodicInfo) string {
	if r.Cadence == "" {
		return "-"
	}
	return fmtTime(&r.NextFireAt)
}

func routineStatusLabel(r daemon.PeriodicInfo) string {
	switch {
	case r.Cadence == "":
		return "on-demand"
	case r.Paused:
		return "paused"
	default:
		return "active"
	}
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func fmtTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}
