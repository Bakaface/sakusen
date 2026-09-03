package main

import (
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/spf13/cobra"
)

var periodicsCmd = &cobra.Command{
	Use:     "periodics",
	Aliases: []string{"periodic"},
	Short:   "Manage periodic (scheduled) task definitions",
	Long: `List and manage periodic task definitions declared under the top-level
periodic: section of .sakusen.yml. Definitions are read-only here (edit the yml
to add/change them); this command surfaces their schedule, pause state, and run
history, and can pause/resume or trigger an immediate run.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPeriodicsList()
	},
}

var periodicsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List periodic definitions for this project",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPeriodicsList()
	},
}

var periodicsShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show a periodic definition",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid periodic ID: %s", args[0])
		}
		return withPeriodicClient(func(c *client.Client) error {
			p, err := c.GetPeriodic(id)
			if err != nil {
				return err
			}
			printPeriodicDetail(p)
			return nil
		})
	},
}

var periodicsPauseCmd = &cobra.Command{
	Use:   "pause <id>",
	Short: "Pause a periodic definition",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setPaused(args[0], true) },
}

var periodicsResumeCmd = &cobra.Command{
	Use:   "resume <id>",
	Short: "Resume a paused periodic definition",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setPaused(args[0], false) },
}

var periodicsRunsCmd = &cobra.Command{
	Use:   "runs <id>",
	Short: "List tasks materialized by a periodic definition",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid periodic ID: %s", args[0])
		}
		return withPeriodicClient(func(c *client.Client) error {
			tasks, err := c.ListPeriodicRuns(id)
			if err != nil {
				return err
			}
			if len(tasks) == 0 {
				fmt.Println("No runs yet for this periodic definition.")
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

var periodicsRunCmd = &cobra.Command{
	Use:   "run <id>",
	Short: "Trigger an immediate one-shot run (does not advance the schedule)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid periodic ID: %s", args[0])
		}
		return withPeriodicClient(func(c *client.Client) error {
			t, err := c.FirePeriodicNow(id)
			if err != nil {
				return err
			}
			fmt.Printf("Fired periodic #%d → task #%d (%s)\n", id, t.ID, t.Title)
			return nil
		})
	},
}

func init() {
	periodicsCmd.AddCommand(periodicsListCmd)
	periodicsCmd.AddCommand(periodicsShowCmd)
	periodicsCmd.AddCommand(periodicsPauseCmd)
	periodicsCmd.AddCommand(periodicsResumeCmd)
	periodicsCmd.AddCommand(periodicsRunsCmd)
	periodicsCmd.AddCommand(periodicsRunCmd)
	rootCmd.AddCommand(periodicsCmd)
}

// withPeriodicClient connects to the daemon, runs fn, and closes the client.
func withPeriodicClient(fn func(*client.Client) error) error {
	c := client.New(cfg)
	if err := c.Connect(); err != nil {
		return fmt.Errorf("daemon not running: %w", err)
	}
	defer c.Close()
	return fn(c)
}

func runPeriodicsList() error {
	return withPeriodicClient(func(c *client.Client) error {
		periodics, err := c.ListPeriodics(cfg.ProjectDir)
		if err != nil {
			return fmt.Errorf("failed to list periodics: %w", err)
		}
		if len(periodics) == 0 {
			fmt.Println("No periodic definitions. Add them under the top-level periodic: section of .sakusen.yml.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tCADENCE\tWORKFLOW\tNEXT FIRE\tLAST FIRE\tSTATUS")
		fmt.Fprintln(w, "--\t----\t-------\t--------\t---------\t---------\t------")
		for _, p := range periodics {
			fmt.Fprintf(w, "#%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
				p.ID, p.Name, p.Cadence, periodicWorkflowLabel(p),
				fmtTime(&p.NextFireAt), fmtTime(p.LastFiredAt), periodicStatusLabel(p))
		}
		w.Flush()
		return nil
	})
}

func setPaused(idStr string, paused bool) error {
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid periodic ID: %s", idStr)
	}
	return withPeriodicClient(func(c *client.Client) error {
		p, err := c.SetPeriodicPaused(id, paused)
		if err != nil {
			return err
		}
		fmt.Printf("Periodic #%d (%s) is now %s\n", p.ID, p.Name, periodicStatusLabel(*p))
		return nil
	})
}

func printPeriodicDetail(p *daemon.PeriodicInfo) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "ID:\t#%d\n", p.ID)
	fmt.Fprintf(w, "Name:\t%s\n", p.Name)
	fmt.Fprintf(w, "Cadence:\t%s\n", p.Cadence)
	fmt.Fprintf(w, "Workflow:\t%s\n", periodicWorkflowLabel(*p))
	if p.Input != "" {
		fmt.Fprintf(w, "Input:\t%s\n", truncateStr(p.Input, 60))
	}
	priority := p.Priority
	if priority == "" {
		priority = "(project default)"
	}
	fmt.Fprintf(w, "Priority:\t%s\n", priority)
	fmt.Fprintf(w, "Status:\t%s\n", periodicStatusLabel(*p))
	fmt.Fprintf(w, "Next fire:\t%s\n", fmtTime(&p.NextFireAt))
	fmt.Fprintf(w, "Last fire:\t%s\n", fmtTime(p.LastFiredAt))
	if p.LastTaskID != nil {
		fmt.Fprintf(w, "Last task:\t#%d\n", *p.LastTaskID)
	}
	w.Flush()
}

func periodicWorkflowLabel(p daemon.PeriodicInfo) string {
	if p.Inline || p.WorkflowRef == "" {
		return "(inline)"
	}
	return p.WorkflowRef
}

func periodicStatusLabel(p daemon.PeriodicInfo) string {
	if p.Paused {
		return "paused"
	}
	return "active"
}

func fmtTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}
