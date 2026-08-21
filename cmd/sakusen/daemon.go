package main

import (
	"fmt"

	"github.com/Bakaface/sakusen/internal/daemon"
	"github.com/spf13/cobra"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Manage the Sakusen daemon",
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon",
	Long:  "Start the Sakusen daemon. The process runs in the foreground; background it with '&' or your service manager.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return daemon.Start(cfg)
	},
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the daemon gracefully",
	RunE: func(cmd *cobra.Command, args []string) error {
		return daemon.Stop(cfg)
	},
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check if daemon is running",
	RunE: func(cmd *cobra.Command, args []string) error {
		running, pid, err := daemon.Status(cfg)
		if err != nil {
			return err
		}
		if running {
			fmt.Printf("Daemon is running (PID: %d)\n", pid)
		} else {
			fmt.Println("Daemon is not running")
		}
		return nil
	},
}
