package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/Bakaface/sakusen/internal/client"
	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/spf13/cobra"
)

var projectsCmd = &cobra.Command{
	Use:   "projects",
	Short: "Manage the daemon's project registry",
	Long: `List and remove the projects the daemon has registered. A project is
registered the first time sakusen is used in it and stays registered until
removed here, even after its directory is deleted.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withRoutineClient(runProjectsList)
	},
}

var projectsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered projects",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withRoutineClient(runProjectsList)
	},
}

var projectsRemoveCmd = &cobra.Command{
	Use:   "remove <path|id>",
	Short: "Remove a project from the registry",
	Long: `Remove a project from the registry, deleting its tasks and everything
recorded for them (steps, contexts, dependency edges, routines, tracks). When
the project directory still exists, the tasks' worktrees, branches and logs are
removed too. A project whose directory is gone can still be removed.

Removal is refused while the project has any task that is not completed,
failed or merge-failed. An all-digit argument is a project id; write a path
like ./123 to mean a directory.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if missing, _ := cmd.Flags().GetBool("missing"); missing {
			return cobra.NoArgs(cmd, args)
		}
		return cobra.ExactArgs(1)(cmd, args)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		yes, _ := cmd.Flags().GetBool("yes")
		missing, _ := cmd.Flags().GetBool("missing")
		return withRoutineClient(func(c *client.Client) error {
			projects, err := c.ListProjects()
			if err != nil {
				return fmt.Errorf("failed to list projects: %w", err)
			}
			if missing {
				return removeMissingProjects(c, projects, yes)
			}
			p, err := resolveProjectRef(projects, args[0])
			if err != nil {
				return err
			}
			if !yes && !confirm(fmt.Sprintf("Remove project #%d %s (%s, %s)? This deletes its tasks and their history.", p.ID, p.Name, p.Path, projectTaskSummary(*p))) {
				fmt.Println("Cancelled")
				return nil
			}
			return removeProject(c, *p)
		})
	},
}

func init() {
	projectsRemoveCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	projectsRemoveCmd.Flags().Bool("missing", false, "Remove every registered project whose directory no longer exists")
	projectsCmd.AddCommand(projectsListCmd)
	projectsCmd.AddCommand(projectsRemoveCmd)
	rootCmd.AddCommand(projectsCmd)
}

func runProjectsList(c *client.Client) error {
	projects, err := c.ListProjects()
	if err != nil {
		return fmt.Errorf("failed to list projects: %w", err)
	}
	if len(projects) == 0 {
		fmt.Println("No registered projects.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tPATH\tEXISTS\tTASKS\tACTIVE")
	fmt.Fprintln(w, "--\t----\t----\t------\t-----\t------")
	for _, p := range projects {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%d\t%d\n", p.ID, p.Name, p.Path, yesNo(p.PathExists), p.Tasks, p.ActiveTasks)
	}
	w.Flush()
	return nil
}

// resolveProjectRef finds the project an argument names: an all-digit ref is
// an id, anything else a path matched as given or symlink-resolved.
func resolveProjectRef(projects []daemon.ProjectInfo, ref string) (*daemon.ProjectInfo, error) {
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		for i := range projects {
			if projects[i].ID == id {
				return &projects[i], nil
			}
		}
		return nil, fmt.Errorf("no registered project with id %d", id)
	}
	abs, err := filepath.Abs(ref)
	if err != nil {
		return nil, err
	}
	candidates := []string{abs}
	if canon := resolveSymlinksOfExisting(abs); canon != abs {
		candidates = append(candidates, canon)
	}
	for _, path := range candidates {
		for i := range projects {
			if projects[i].Path == path {
				return &projects[i], nil
			}
		}
	}
	return nil, fmt.Errorf("no registered project at %s", abs)
}

// resolveSymlinksOfExisting resolves symlinks in the longest existing prefix
// of abs and re-appends the rest, so a deleted project directory still maps
// to the canonical path it was registered under.
func resolveSymlinksOfExisting(abs string) string {
	var rest []string
	for dir := abs; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(append([]string{resolved}, rest...)...)
		}
		if filepath.Dir(dir) == dir {
			return abs
		}
		rest = append([]string{filepath.Base(dir)}, rest...)
	}
}

func removeMissingProjects(c *client.Client, projects []daemon.ProjectInfo, yes bool) error {
	var gone []daemon.ProjectInfo
	for _, p := range projects {
		if !p.PathExists {
			gone = append(gone, p)
		}
	}
	if len(gone) == 0 {
		fmt.Println("No registered project has a missing directory.")
		return nil
	}
	fmt.Println("Projects whose directory no longer exists:")
	for _, p := range gone {
		fmt.Printf("  #%d %s (%s, %s)\n", p.ID, p.Name, p.Path, projectTaskSummary(p))
	}
	if !yes && !confirm(fmt.Sprintf("Remove these %d project(s)? This deletes their tasks and their history.", len(gone))) {
		fmt.Println("Cancelled")
		return nil
	}
	failed := 0
	for _, p := range gone {
		if err := removeProject(c, p); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d project(s) not removed", failed, len(gone))
	}
	return nil
}

func removeProject(c *client.Client, p daemon.ProjectInfo) error {
	resp, err := c.RemoveProject(p.ID)
	if err != nil {
		return err
	}
	fmt.Printf("Removed project #%d %s (%s), %d task(s) deleted\n", resp.Project.ID, resp.Project.Name, resp.Project.Path, resp.TasksRemoved)
	return nil
}

func projectTaskSummary(p daemon.ProjectInfo) string {
	s := fmt.Sprintf("%d task(s)", p.Tasks)
	if p.ActiveTasks > 0 {
		s += fmt.Sprintf(", %d active", p.ActiveTasks)
	}
	if !p.PathExists {
		s += ", directory missing"
	}
	return s
}

// confirm asks a yes/no question on stdin; only "y" or "yes" accepts.
func confirm(question string) bool {
	fmt.Printf("%s [y/N] ", question)
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes"
}
