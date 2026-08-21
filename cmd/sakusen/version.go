package main

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Version is set at build time via `-ldflags "-X main.Version=$TAG"`.
// When unset, it falls back to the module version embedded by `go install`.
var Version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the sakusen version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(resolveVersion())
	},
}

func resolveVersion() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return Version
}
