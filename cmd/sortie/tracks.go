package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Bakaface/sortie/internal/client"
	"github.com/Bakaface/sortie/internal/daemon"
	"github.com/spf13/cobra"
)

var tracksCmd = &cobra.Command{
	Use:   "tracks",
	Short: "Manage tracks (named, hierarchical context containers for tasks)",
}

// trackClient dials the daemon and returns a connected client. The caller must
// Close() it.
func trackClient() (*client.Client, error) {
	c := client.New(cfg)
	if err := c.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to daemon: %w", err)
	}
	return c, nil
}

var tracksCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new track",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		parent, _ := cmd.Flags().GetString("parent")
		global, _ := cmd.Flags().GetBool("global")
		workflow, _ := cmd.Flags().GetString("workflow")
		context, _ := cmd.Flags().GetString("context")
		description, _ := cmd.Flags().GetString("description")

		c, err := trackClient()
		if err != nil {
			return err
		}
		defer c.Close()

		track, err := c.CreateTrack(daemon.CreateTrackRequest{
			Name:        args[0],
			ParentTrack: parent,
			Global:      global,
			Workflow:    workflow,
			Context:     context,
			Description: description,
			ProjectPath: cfg.ProjectDir,
		})
		if err != nil {
			return fmt.Errorf("failed to create track: %w", err)
		}

		fmt.Printf("Created track #%d (%s)\n", track.ID, track.Slug)
		return nil
	},
}

var tracksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List tracks visible from this project (project + global)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOut, _ := cmd.Flags().GetBool("json")

		c, err := trackClient()
		if err != nil {
			return err
		}
		defer c.Close()

		tracks, err := c.ListTracks(cfg.ProjectDir)
		if err != nil {
			return fmt.Errorf("failed to list tracks: %w", err)
		}

		if jsonOut {
			return writeJSON(os.Stdout, tracks)
		}

		if len(tracks) == 0 {
			fmt.Println("No tracks found. Create one with 'sortie tracks create <name>'.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tSLUG\tSCOPE\tPARENT\tWORKFLOW\tDESCRIPTION\tSIZE")
		fmt.Fprintln(w, "--\t----\t-----\t------\t--------\t-----------\t----")
		for _, tr := range tracks {
			scope := "project"
			if tr.Global {
				scope = "global"
			}
			parent := tr.ParentSlug
			if parent == "" && tr.ParentID != nil {
				parent = fmt.Sprintf("#%d", *tr.ParentID)
			}
			if parent == "" {
				parent = "-"
			}
			workflow := tr.Workflow
			if workflow == "" {
				workflow = "-"
			}
			// Descriptions are free-form and may be multi-line (set-description
			// reads stdin), so collapse whitespace before printing — a raw
			// newline would break the row into two mis-aligned ones.
			description := truncateStr(strings.Join(strings.Fields(tr.Description), " "), 40)
			if description == "" {
				description = "-"
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%d\n", tr.ID, tr.Slug, scope, parent, workflow, description, tr.ContextLen)
		}
		w.Flush()
		return nil
	},
}

var tracksShowCmd = &cobra.Command{
	Use:   "show <slug-or-id>",
	Short: "Show a track's metadata and its rendered {{track.context}}",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOut, _ := cmd.Flags().GetBool("json")

		c, err := trackClient()
		if err != nil {
			return err
		}
		defer c.Close()

		resp, err := c.GetTrack(cfg.ProjectDir, args[0])
		if err != nil {
			return fmt.Errorf("failed to get track: %w", err)
		}

		if jsonOut {
			return writeJSON(os.Stdout, resp)
		}

		tr := resp.Track
		scope := "project"
		if tr.Global {
			scope = "global"
		}
		fmt.Printf("Track #%d\n", tr.ID)
		fmt.Printf("  Name:        %s\n", tr.Name)
		fmt.Printf("  Slug:        %s\n", tr.Slug)
		fmt.Printf("  Scope:       %s\n", scope)
		if tr.Description != "" {
			fmt.Printf("  Description: %s\n", tr.Description)
		}
		if tr.ParentSlug != "" {
			fmt.Printf("  Parent:      %s\n", tr.ParentSlug)
		} else if tr.ParentID != nil {
			fmt.Printf("  Parent:      #%d\n", *tr.ParentID)
		}
		if tr.Workflow != "" {
			fmt.Printf("  Workflow:    %s\n", tr.Workflow)
		}
		fmt.Printf("  Size:        %d bytes\n", tr.ContextLen)
		fmt.Printf("  Created:     %s\n", tr.CreatedAt.Format(time.RFC3339))
		fmt.Printf("  Updated:     %s\n", tr.UpdatedAt.Format(time.RFC3339))
		if resp.RenderedContext != "" {
			// The daemon-rendered chain — exactly what {{track.context}} yields.
			fmt.Printf("\n%s\n", resp.RenderedContext)
		}
		return nil
	},
}

var tracksSetContextCmd = &cobra.Command{
	Use:   "set-context <slug-or-id> [context]",
	Short: "Replace (or --append to) a track's own context",
	Long: `Set a track's own context. The context can be provided as a positional
argument or via stdin. By default the existing context is replaced; pass
--append to concatenate with a blank-line separator instead.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		var context string
		if len(args) == 2 {
			context = args[1]
		} else {
			// Read from stdin if no argument provided (mirrors sortie create).
			stat, err := os.Stdin.Stat()
			if err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
				scanner := bufio.NewScanner(os.Stdin)
				var lines []string
				for scanner.Scan() {
					lines = append(lines, scanner.Text())
				}
				context = strings.Join(lines, "\n")
			}
		}

		appendMode, _ := cmd.Flags().GetBool("append")
		mode := "replace"
		if appendMode {
			mode = "append"
		}

		if strings.TrimSpace(context) == "" && mode == "append" {
			return fmt.Errorf("context is required (provide as argument or via stdin)")
		}

		c, err := trackClient()
		if err != nil {
			return err
		}
		defer c.Close()

		if err := c.SetTrackContext(cfg.ProjectDir, args[0], context, mode); err != nil {
			return fmt.Errorf("failed to set track context: %w", err)
		}

		fmt.Printf("Track %s context updated (%s)\n", args[0], mode)
		return nil
	},
}

var tracksSetDescriptionCmd = &cobra.Command{
	Use:   "set-description <slug-or-id> [description]",
	Short: "Replace a track's description",
	Long: `Set a track's description — the stable one-liner stating what the track is
for, which routing agents read when picking a track. The description can be
provided as a positional argument or via stdin, and always replaces the
existing one (an empty value clears it). Use set-context for the accumulating
working context instead.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		var description string
		if len(args) == 2 {
			description = args[1]
		} else {
			// Read from stdin if no argument provided (mirrors sortie create).
			stat, err := os.Stdin.Stat()
			if err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
				scanner := bufio.NewScanner(os.Stdin)
				var lines []string
				for scanner.Scan() {
					lines = append(lines, scanner.Text())
				}
				description = strings.Join(lines, "\n")
			}
		}

		c, err := trackClient()
		if err != nil {
			return err
		}
		defer c.Close()

		if err := c.SetTrackDescription(cfg.ProjectDir, args[0], description); err != nil {
			return fmt.Errorf("failed to set track description: %w", err)
		}

		fmt.Printf("Track %s description updated\n", args[0])
		return nil
	},
}
