package main

import "testing"

func TestTracksCmd_Registered(t *testing.T) {
	found := false
	for _, c := range rootCmd.Commands() {
		if c.Name() == "tracks" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected 'tracks' command registered on root")
	}

	subs := map[string]bool{}
	for _, c := range tracksCmd.Commands() {
		subs[c.Name()] = true
	}
	for _, want := range []string{"create", "list", "show", "set-context"} {
		if !subs[want] {
			t.Errorf("expected 'tracks %s' subcommand", want)
		}
	}
}

func TestTracksCmd_Flags(t *testing.T) {
	for _, name := range []string{"parent", "global", "workflow", "context"} {
		if tracksCreateCmd.Flag(name) == nil {
			t.Errorf("expected --%s on tracks create", name)
		}
	}
	if tracksSetContextCmd.Flag("append") == nil {
		t.Error("expected --append on tracks set-context")
	}
	if tracksListCmd.Flag("json") == nil || tracksShowCmd.Flag("json") == nil {
		t.Error("expected --json on tracks list/show")
	}
}

func TestCreateCmd_TrackFlagParses(t *testing.T) {
	if createCmd.Flag("track") == nil {
		t.Fatal("expected --track flag on create command")
	}
}
