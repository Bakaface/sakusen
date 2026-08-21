package main

import (
	"os"
	"path/filepath"

	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	gitpkg "github.com/Bakaface/sakusen/internal/git"
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

	if _, err := os.Stat(filepath.Join(cwd, ".sakusen.yml")); err != nil {
		// No .sakusen.yml — check if we're in a git repo to filter by repo name
		repoRoot, err := gitpkg.GetRepoRoot(cwd)
		if err != nil {
			return 0, "", "", true, true, 0, ""
		}
		// Must match config.ProjectNameFromPath used by GetOrCreateProject when
		// the row was inserted; otherwise dot-prefixed dirs (e.g. ".pai") store
		// as "_pai" but get queried as ".pai" → empty task list.
		repoName := config.ProjectNameFromPath(repoRoot)
		return 0, repoRoot, repoName, false, true, 0, ""
	}

	repoRoot, err := gitpkg.GetRepoRoot(cwd)
	if err != nil {
		return 0, cwd, "", false, true, 0, ""
	}

	dbPath := cfg.GetDatabasePath("")
	database, err := db.Open(dbPath)
	if err != nil {
		return 0, repoRoot, "", false, true, 0, ""
	}
	defer database.Close()

	proj, err := database.GetOrCreateProject(repoRoot)
	if err != nil {
		return 0, repoRoot, "", false, true, 0, ""
	}

	return proj.ID, repoRoot, config.ProjectNameFromPath(repoRoot), false, proj.DefaultWorktree, proj.DefaultBranchMode, proj.DefaultWorkflow
}
