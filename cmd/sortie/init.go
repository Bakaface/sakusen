package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Bakaface/sortie/internal/config"
	gitpkg "github.com/Bakaface/sortie/internal/git"
	"github.com/Bakaface/sortie/internal/runner"
	"github.com/spf13/cobra"
)

// scaffoldFS embeds the files `sortie init` copies into a fresh project:
// the .sortie.yml template and the user-owned agent scripts under
// .sortie/agents/ (default claude agent records). Scaffolded copies are
// user-owned and never auto-upgrade.
//
//go:embed scaffold
var scaffoldFS embed.FS

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize Sortie in the current project",
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}

		if !gitpkg.IsGitRepo(cwd) {
			return fmt.Errorf("not a git repository")
		}

		configPath := filepath.Join(cwd, ".sortie.yml")
		if _, err := os.Stat(configPath); err == nil {
			fmt.Println(".sortie.yml already exists")
			return nil
		}

		sortieDir := filepath.Join(cwd, ".sortie")
		if err := os.MkdirAll(sortieDir, 0755); err != nil {
			return fmt.Errorf("failed to create .sortie directory: %w", err)
		}

		detected := config.DetectProject(cwd)

		// Write the config template.
		tmpl, err := scaffoldFS.ReadFile("scaffold/sortie.yml")
		if err != nil {
			return fmt.Errorf("failed to read embedded config template: %w", err)
		}
		if err := os.WriteFile(configPath, tmpl, 0644); err != nil {
			return fmt.Errorf("failed to write .sortie.yml: %w", err)
		}

		// Copy the agent scripts (skip files the user already owns).
		agentsDir := filepath.Join(sortieDir, "agents")
		if err := os.MkdirAll(agentsDir, 0755); err != nil {
			return fmt.Errorf("failed to create .sortie/agents: %w", err)
		}
		var scaffolded []string
		err = fs.WalkDir(scaffoldFS, "scaffold/agents", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			dst := filepath.Join(agentsDir, filepath.Base(path))
			if _, statErr := os.Stat(dst); statErr == nil {
				return nil // user-owned file already present — never overwrite
			}
			data, readErr := scaffoldFS.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			mode := os.FileMode(0644)
			if strings.HasSuffix(path, ".sh") {
				mode = 0755
			}
			if writeErr := os.WriteFile(dst, data, mode); writeErr != nil {
				return writeErr
			}
			scaffolded = append(scaffolded, dst)
			return nil
		})
		if err != nil {
			return fmt.Errorf("failed to scaffold agent scripts: %w", err)
		}

		// Keep sortie's runtime artifacts out of git status: the .sortie/ work
		// dir and the per-worktree raw agent output log.
		if err := ensureGitignoreEntries(cwd, []string{".sortie/", runner.OutputLogFileName}); err != nil {
			fmt.Printf("Warning: failed to update .gitignore: %v\n", err)
		}

		fmt.Printf("Initialized Sortie\n")
		fmt.Printf("  Config: %s\n", configPath)
		fmt.Printf("  Agents: %s/ (%d scaffolded scripts — user-owned, edit freely)\n", agentsDir, len(scaffolded))
		fmt.Printf("Detected project type: %s\n", detected.Type)
		if detected.Commands.Test != "" {
			fmt.Printf("  Test command: %s\n", detected.Commands.Test)
		}
		if detected.Commands.Lint != "" {
			fmt.Printf("  Lint command: %s\n", detected.Commands.Lint)
		}
		fmt.Println("Note: the scaffolded agent scripts require `claude` and `jq` on PATH. Steps reference them via $SORTIE_PROJECT_PATH, so worktrees share the project-root copies (`git add -f .sortie/agents` to share them with teammates).")

		return nil
	},
}

// ensureGitignoreEntries appends the given entries to the repo's .gitignore
// when they are not already present (exact-line match). Creates the file if
// missing.
func ensureGitignoreEntries(repoRoot string, entries []string) error {
	path := filepath.Join(repoRoot, ".gitignore")
	existing := map[string]bool{}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		existing[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, e := range entries {
		if !existing[e] {
			missing = append(missing, e)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	out := string(data)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += strings.Join(missing, "\n") + "\n"
	return os.WriteFile(path, []byte(out), 0644)
}
