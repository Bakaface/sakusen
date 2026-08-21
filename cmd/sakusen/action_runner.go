package main

import (
	"fmt"

	"github.com/Bakaface/sakusen/internal/action"
	"github.com/Bakaface/sakusen/internal/client"
	"github.com/spf13/cobra"
)

// runAction is the CLI's shared dispatcher to internal/action. It opens a
// client connection, builds an action.Ctx, invokes the verb, and prints the
// Result.Message on success. Verbs that don't need a daemon (e.g. validate)
// can call action.Run<Verb> directly without this helper.
func runAction(cmd *cobra.Command, fn func(action.Ctx) (action.Result, error)) error {
	c := client.New(cfg)
	if err := c.Connect(); err != nil {
		return fmt.Errorf("failed to connect to daemon: %w", err)
	}
	defer c.Close()

	out := cmd.OutOrStdout()
	actx := action.Ctx{Cfg: cfg, Client: c, Out: out}
	res, err := fn(actx)
	if err != nil {
		return err
	}
	if res.Message != "" {
		fmt.Fprintln(out, res.Message)
	}
	return nil
}
