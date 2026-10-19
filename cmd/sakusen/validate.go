package main

import (
	"fmt"

	"github.com/Bakaface/sakusen/internal/action"
	"github.com/spf13/cobra"
)

var validateCmd = &cobra.Command{
	Use:   "validate [path]",
	Short: "Validate a .sakusen.yml configuration file",
	Long: `Validate a Sakusen .sakusen.yml configuration file.

Checks YAML syntax, flags unknown top-level fields, and runs the same
workflow validation the daemon performs at load time (loop targets, step
names, summarization strategies, enum values).

By default the file is merged with the global tier (~/.config/sakusen/config.yaml,
~/.sakusen.yml, ~/.sakusen/workflows, track workflows) as the daemon does, and
agent references, alias targets and stacked variant compositions are resolved
against the merged agent registry. A global config that fails to load fails
validation. --file-only checks the single file without resolving cross-tier
references.

With no argument, validates ./.sakusen.yml. Otherwise validates the given path.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var p string
		if len(args) == 1 {
			p = args[0]
		}
		fileOnly, _ := cmd.Flags().GetBool("file-only")
		res, err := action.RunValidate(action.Ctx{Cfg: cfg, Out: cmd.OutOrStdout()}, action.ValidateArgs{Path: p, FileOnly: fileOnly})
		if err != nil {
			return err
		}
		if res.Message != "" {
			fmt.Fprintln(cmd.OutOrStdout(), res.Message)
		}
		return nil
	},
}
