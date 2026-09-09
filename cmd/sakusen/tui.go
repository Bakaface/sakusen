package main

import (
	"os"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	"github.com/Bakaface/sakusen/internal/tui"
	"github.com/spf13/cobra"
)

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch the TUI (connects to daemon)",
	RunE: func(cmd *cobra.Command, args []string) error {
		globalFlag, _ := cmd.Flags().GetBool("global")

		projectID, projectPath, projectName, globalMode, defaultWorktree, defaultBranchMode, defaultWorkflow := resolveProjectMode(globalFlag)
		return tui.Run(cfg, projectID, projectPath, projectName, globalMode, defaultWorktree, defaultBranchMode, defaultWorkflow)
	},
}

func resolveProjectMode(globalFlag bool) (projectID int64, projectPath string, projectName string, globalMode bool, defaultWorktree bool, defaultBranchMode int, defaultWorkflow string) {
	if globalFlag {
		return 0, "", "", true, true, 0, ""
	}

	cwd, err := os.Getwd()
	if err != nil {
		return 0, "", "", true, true, 0, ""
	}

	root, kind, err := config.FindProjectRoot(cwd)
	if err != nil || kind == config.ProjectRootNone {
		return 0, "", "", true, true, 0, ""
	}

	if kind == config.ProjectRootGitToplevel {
		// No .sakusen.yml anywhere up to the git toplevel — filter by repo name
		// instead of registering a project row. The name must match
		// config.ProjectNameFromPath used by GetOrCreateProject when the row was
		// inserted; otherwise dot-prefixed dirs (e.g. ".pai") store as "_pai" but
		// get queried as ".pai" → empty task list.
		return 0, root, config.ProjectNameFromPath(root), false, true, 0, ""
	}

	dbPath := cfg.GetDatabasePath("")
	database, err := db.Open(dbPath)
	if err != nil {
		return 0, root, "", false, true, 0, ""
	}
	defer database.Close()

	proj, err := database.GetOrCreateProject(root)
	if err != nil {
		return 0, root, "", false, true, 0, ""
	}

	return proj.ID, root, config.ProjectNameFromPath(root), false, proj.DefaultWorktree, proj.DefaultBranchMode, proj.DefaultWorkflow
}
